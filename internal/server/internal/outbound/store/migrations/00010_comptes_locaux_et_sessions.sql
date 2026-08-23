-- Les comptes locaux et leurs sessions.
--
-- Jusqu'ici l'authentification n'existait pas : le middleware refusait TOUTE
-- requête, sauf derrière `WARDEN_INSECURE_DEV_AUTH=1` où il croyait un en-tête
-- `X-User-Subject` sur parole. L'audit disait « alice » parce qu'un client
-- l'avait affirmé, ce que le README nomme comme la deuxième lacune du produit.
--
-- Ces deux tables ne remplacent pas OIDC — elles donnent au produit une identité
-- VÉRIFIABLE en attendant, et le point important est qu'elles ne changent rien
-- à la table `users` : un compte local est un `users.type = 'local'`, exactement
-- comme un compte OIDC sera un `users.type = 'oidc'`. Le jour où OIDC arrive,
-- ces tables cessent d'être alimentées et rien d'autre ne bouge.

-- +goose Up

-- ---------------------------------------------------------------------------
-- local_credential
--
-- Une table SÉPARÉE de `users`, et ce n'est pas de la normalisation pour la
-- forme. Trois raisons, la dernière décidant :
--
--   1. seuls les comptes `local` ont un mot de passe ; la colonne serait NULL
--      sur toute ligne OIDC ou LDAP, c'est-à-dire sur la majorité à terme ;
--   2. `users` est lue par tout ce qui affiche un acteur dans l'audit, donc par
--      les chemins les plus fréquents du produit ;
--   3. surtout : `warden_reader` a SELECT sur `users` pour que l'interface
--      d'audit nomme les acteurs. Une empreinte de mot de passe dans cette table
--      serait donc lisible par le rôle de LECTURE, et une empreinte lisible est
--      une empreinte attaquable hors ligne. La séparer est ce qui permet de ne
--      lui accorder aucun droit ici.
-- ---------------------------------------------------------------------------

CREATE TABLE local_credential (
    -- La clé primaire EST la clé étrangère : un compte a au plus un mot de
    -- passe. Pas d'`id` propre, qui n'aurait servi qu'à rendre exprimable un
    -- second mot de passe pour le même compte.
    user_id bigint PRIMARY KEY REFERENCES users (id),

    -- Une chaîne au format PHC : `$argon2id$v=19$m=...,t=...,p=...$sel$empreinte`.
    --
    -- Les PARAMÈTRES voyagent avec l'empreinte, et c'est ce qui rend leur
    -- durcissement possible : le jour où le coût de calcul doit monter, les
    -- empreintes déjà écrites restent vérifiables et se réécrivent à la
    -- prochaine connexion. Une colonne de paramètres à côté aurait figé le
    -- format pour toute la table à la fois.
    --
    -- Le nom `password_hash` dit ce que c'est. Ce n'est PAS un secret au sens
    -- des credentials d'accès — on ne peut pas s'en servir pour se connecter —
    -- mais c'est du matériel d'attaque hors ligne, d'où les GRANT plus bas.
    password_hash text NOT NULL CHECK (password_hash LIKE '$argon2id$%'),

    updated_at timestamptz NOT NULL DEFAULT now(),

    -- Qui a posé ce mot de passe. NULL quand c'est l'amorçage — il n'y a alors
    -- aucun utilisateur pour le porter, et désigner quelqu'un serait faux.
    updated_by bigint REFERENCES users (id)
);

COMMENT ON TABLE local_credential IS
    'Comptes locaux, en attendant OIDC. Séparée de `users` pour que `warden_reader` n''y ait aucun droit.';
COMMENT ON COLUMN local_credential.password_hash IS
    'argon2id au format PHC. Les paramètres voyagent avec l''empreinte, pour pouvoir être durcis.';

-- ---------------------------------------------------------------------------
-- session
--
-- Ce que `warden login` obtient et que la CLI présente ensuite en
-- `Authorization: Bearer`.
--
-- L'EMPREINTE est stockée, jamais le jeton. C'est la règle que l'ADR-0023 pose
-- déjà pour le jeton d'amorçage des pods : l'authentification est une recherche
-- par empreinte, aucun secret n'est comparé octet à octet, et compromettre une
-- sauvegarde de la base ne donne aucun jeton utilisable.
-- ---------------------------------------------------------------------------

CREATE TABLE session (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    user_id bigint NOT NULL REFERENCES users (id),

    -- SHA-256 du jeton, 32 octets. `bytea` et non `text` : c'est une empreinte
    -- binaire, et la stocker en hexadécimal doublerait l'index de la colonne la
    -- plus consultée de la table.
    token_hash bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),

    created_at timestamptz NOT NULL DEFAULT now(),

    -- Expiration ABSOLUE, posée à la création. Pas de fenêtre glissante : elle
    -- exigerait une écriture à chaque requête authentifiée, donc une écriture
    -- sur le chemin le plus chaud du produit, pour n'acheter que le confort de
    -- ne pas se reconnecter. `warden login` est une commande.
    expires_at timestamptz NOT NULL,

    -- Rafraîchi PARESSEUSEMENT — au plus une fois par heure — pour la même
    -- raison : savoir qu'une session sert encore vaut une écriture par heure,
    -- pas une par requête.
    last_seen_at timestamptz NOT NULL DEFAULT now(),

    -- Révocation explicite : `warden logout`, ou un administrateur qui coupe
    -- l'accès. Pas de DELETE — savoir qu'une session a existé et quand elle a
    -- été coupée fait partie de ce qu'un audit doit pouvoir dire.
    revoked_at timestamptz,

    CONSTRAINT session_expire_apres_creation CHECK (expires_at > created_at)
);

-- L'index qui porte l'authentification de CHAQUE requête. Partiel sur les
-- sessions vivantes : les sessions révoquées et expirées s'accumulent
-- indéfiniment — voir le commentaire de `revoked_at` — et n'ont aucune raison
-- de peser sur la recherche.
--
-- `token_hash` est déjà UNIQUE donc déjà indexé ; celui-ci le double
-- délibérément avec une condition, ce que la contrainte d'unicité ne peut pas
-- porter.
CREATE INDEX session_vivantes_idx
    ON session (token_hash)
    WHERE revoked_at IS NULL;

-- Retrouver les sessions d'un compte : c'est ce qu'il faut pour toutes les
-- couper d'un coup quand un mot de passe change ou qu'un compte est désactivé.
CREATE INDEX session_par_compte_idx ON session (user_id) WHERE revoked_at IS NULL;

COMMENT ON TABLE session IS
    'Sessions de la CLI. Seule l''empreinte du jeton est stockée (même règle que runner_pod).';
COMMENT ON COLUMN session.expires_at IS
    'Expiration absolue. Pas de fenêtre glissante : elle coûterait une écriture par requête.';

-- ---------------------------------------------------------------------------
-- GRANT
--
-- RIEN pour `warden_reader` sur `local_credential` : c'est la raison d'être de
-- la séparation d'avec `users` (voir plus haut). Une empreinte lisible par le
-- rôle de lecture serait une empreinte attaquable hors ligne.
--
-- RIEN pour `warden_reader` sur `session` non plus : savoir qui a une session
-- ouverte n'est pas une question d'audit d'exécution, et la table porte des
-- empreintes de jetons vivants.
--
-- RIEN pour `warden_ingest` sur ni l'une ni l'autre : l'ingestion écrit des
-- events depuis un pod hostile. Elle n'a aucune raison de connaître les comptes,
-- et moins encore leurs sessions.
-- ---------------------------------------------------------------------------

-- +goose Down

DROP TABLE IF EXISTS session;
DROP TABLE IF EXISTS local_credential;
