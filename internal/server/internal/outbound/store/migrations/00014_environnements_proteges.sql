-- Les options par environnement (ADR-0044) : un avertissement visuel et un
-- gel opérationnel, PAS un contrôle d'accès.
--
-- Jusqu'ici, RIEN n'était déclaré : le badge du navigateur reconnaissait
-- `prod` en comparant le nom, câblé en dur — voir `env-badge.ts` avant ce
-- commit. Un environnement peut s'appeler autrement et mériter la même
-- prudence visuelle, ou avoir besoin d'être gelé temporairement sans toucher
-- à qui a le droit d'y lancer un run.
--
-- CE N'EST PAS UN CONTRÔLE D'ACCÈS À UN CREDENTIAL. Voir `environment_grant`
-- (migration 00011, ADR-0033) pour ça. `protected` n'est qu'un avertissement
-- visuel. `allow_run` est un GEL, volontairement indépendant des droits
-- individuels : geler `prod` n'y retire l'accès de personne, et dégeler ne
-- rend l'accès à personne — les droits restent ce qu'ils étaient. Ni l'une ni
-- l'autre colonne n'a de date de révocation ni d'auteur par ligne :
-- contrairement aux droits, il n'y a ici aucune question d'audit du type « qui
-- avait ce droit le 3 mars » — le contrôleur remplace la ligne entière à
-- chaque réconciliation, et l'historique du fichier versionné suffit à
-- répondre « quand est-ce que prod a été gelé ».

-- +goose Up

CREATE TABLE protected_environment (
    -- L'environnement est la clé : pas d'`id` de substitution, puisqu'il n'y a
    -- jamais qu'une ligne par environnement et aucune histoire à conserver par
    -- ligne (voir l'en-tête).
    environment text PRIMARY KEY
        CONSTRAINT protected_environment_environment_forme
            CHECK (environment ~ '^[a-z0-9][a-z0-9-]{0,54}$'),

    -- Avertissement visuel du badge du navigateur. Sans conséquence
    -- opérationnelle.
    protected boolean NOT NULL DEFAULT false,

    -- Gel du lancement de run, INDÉPENDANT des droits par environnement.
    -- `true` par défaut : l'ABSENCE d'une ligne, comme la valeur par défaut
    -- d'une ligne existante, doit se lire « autorisé ». C'est le cœur
    -- (`core.VerifierRunAutorise`) qui traite une ligne absente comme
    -- autorisée ; cette colonne ne sert qu'aux lignes qui EXISTENT.
    allow_run boolean NOT NULL DEFAULT true,

    declared_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE protected_environment IS
    'Options par environnement (ADR-0044) : badge visuel (protected) et gel du lancement de run (allow_run) — AUCUNE des deux n''est un contrôle d''accès à un credential.';
COMMENT ON COLUMN protected_environment.allow_run IS
    'false = lancement de run refusé (core.ErrRunBloque), indépendamment des droits par environnement (ADR-0033).';
COMMENT ON COLUMN protected_environment.declared_at IS
    'Depuis quand cette ligne existe en base. Pour « depuis quand déclaré dans le fichier », voir l''historique git du dépôt de déploiement.';

-- GRANT.
--
-- `warden_reader` voit la table : rien ici n'est sensible, ce sont des noms
-- d'environnement déjà visibles partout ailleurs dans le produit.
--
-- RIEN pour `warden_ingest`, pour la même raison que `environment_grant` :
-- l'ingestion écrit des events depuis un pod hostile, elle n'a aucune raison
-- de lire cette table.
GRANT SELECT ON protected_environment TO warden_reader;

-- +goose Down

DROP TABLE IF EXISTS protected_environment;
