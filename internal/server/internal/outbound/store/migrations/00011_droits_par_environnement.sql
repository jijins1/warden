-- Les droits d'accès par environnement (§10.2).
--
-- Jusqu'ici, RIEN. `POST /runs` lisait l'environnement du corps de la requête,
-- l'écrivait tel quel et lançait : n'importe quel compte authentifié pouvait
-- lancer un run sur `prod`, et le control plane lui livrait alors la clé SSH de
-- `prod` dans son pod.
--
-- CE QUE CETTE TABLE ARRÊTE, et ce qu'elle n'arrête pas, doit être dit
-- exactement — le §10.2 classe le filtrage côté serveur en « confort, pas
-- sécurité », et un contrôle qui ressemble à un contrôle sans en être un est
-- pire que pas de contrôle du tout :
--
--   ARRÊTE  : obtenir la clé d'un environnement auquel on n'a pas droit. Ce
--             n'est PAS que du confort — la clé est délivrée par environnement
--             (ADR-0020), donc sans droit il n'y a pas de credential.
--
--   N'ARRÊTE PAS : qu'un run légitime de `staging` sorte du périmètre de
--             `staging`. L'egress du runner est ouvert (ADR-0030), et le §10.2
--             pose que « le périmètre d'un utilisateur est appliqué par le
--             réseau ou il ne l'est pas ». Cette table est la moitié qui manque
--             au réseau, pas son remplacement.
--
-- Le modèle du §10.2 est `(utilisateur|groupe) → environnement → scope d'hôtes
-- → mode`. Seul l'UTILISATEUR est traité, comme `is_admin` avant lui : les
-- groupes viendront avec l'annuaire qui les porte, et le scope d'hôtes avec le
-- réseau qui l'appliquerait.

-- +goose Up

CREATE TABLE environment_grant (
    -- Un `id` de substitution plutôt que la paire en clé primaire : la
    -- révocation est une DATE et non une suppression (voir `revoked_at`), donc
    -- une même paire peut apparaître plusieurs fois dans l'histoire.
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    user_id bigint NOT NULL REFERENCES users (id),

    -- La même contrainte de forme que `ssh_key.environment`, et pour la même
    -- raison : cette valeur devient un composant de nom de Secret Kubernetes.
    -- Accorder un droit sur un environnement que le magasin de clés ne saura pas
    -- nommer donnerait un droit qui échoue à la livraison.
    environment text NOT NULL
        CONSTRAINT environment_grant_environment_forme
            CHECK (environment ~ '^[a-z0-9][a-z0-9-]{0,54}$'),

    granted_at timestamptz NOT NULL DEFAULT now(),

    -- Qui a accordé. NULL pour les droits repris de l'historique par cette
    -- migration : personne ne les a accordés, et désigner quelqu'un serait faux.
    granted_by bigint REFERENCES users (id),

    -- Révocation DATÉE, jamais un DELETE. « Qui avait accès à prod le 3 mars »
    -- est une question d'audit, et une ligne supprimée ne peut pas y répondre.
    -- C'est la même règle que `users.disabled_at` et `session.revoked_at`.
    revoked_at timestamptz,

    CONSTRAINT environment_grant_revoque_apres CHECK (revoked_at IS NULL OR revoked_at >= granted_at)
);

-- L'unicité porte sur les droits VIVANTS seulement : réaccorder après une
-- révocation doit marcher, et une contrainte sur la paire nue l'interdirait.
CREATE UNIQUE INDEX environment_grant_vivants_idx
    ON environment_grant (user_id, environment)
    WHERE revoked_at IS NULL;

-- La question posée à CHAQUE création de run et à chaque livraison d'ordre.
CREATE INDEX environment_grant_par_compte_idx
    ON environment_grant (user_id)
    WHERE revoked_at IS NULL;

COMMENT ON TABLE environment_grant IS
    'Droits par environnement (§10.2). Sans droit, pas de clé d''accès — mais l''egress reste ouvert.';
COMMENT ON COLUMN environment_grant.revoked_at IS
    'Révocation datée, jamais un DELETE : « qui avait accès à prod le 3 mars » est une question d''audit.';
COMMENT ON COLUMN environment_grant.granted_by IS
    'NULL = repris de l''historique par la migration 00011. Personne ne l''a accordé.';

-- ---------------------------------------------------------------------------
-- Reprise de l'historique.
--
-- Sans elle, cette migration COUPE tout le monde : la table naît vide, et le
-- premier run de chaque utilisateur échoue sur un droit qui n'a jamais existé.
-- Une migration qui casse silencieusement tous ses utilisateurs est une mauvaise
-- migration, quel que soit le bien-fondé du contrôle qu'elle ajoute.
--
-- Ce que ces lignes disent est exactement : « ceci était déjà possible ». Ce
-- n'est PAS un jugement sur ce qui aurait dû l'être — d'où `granted_by` NULL, qui
-- se lit « personne ne l'a décidé ». Un opérateur qui veut repartir d'une page
-- blanche révoque et réaccorde ; il le fera en connaissance de cause.
-- ---------------------------------------------------------------------------
INSERT INTO environment_grant (user_id, environment, granted_at)
SELECT user_id, environment, min(started_at)
  FROM run
 WHERE environment ~ '^[a-z0-9][a-z0-9-]{0,54}$'
 GROUP BY user_id, environment
ON CONFLICT DO NOTHING;

-- GRANT.
--
-- `warden_reader` voit la table : « qui avait accès à quoi » est une question
-- d'audit, et rien ici n'est sensible — ce sont des droits, pas des credentials.
--
-- RIEN pour `warden_ingest` : l'ingestion écrit des events depuis un pod
-- hostile, elle n'a aucune raison de connaître les droits de qui que ce soit.
GRANT SELECT ON environment_grant TO warden_reader;

-- +goose Down

DROP TABLE IF EXISTS environment_grant;
