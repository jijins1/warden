-- Runs planifiés (ADR-0057) : un playbook de la principale, lancé à heure fixe
-- au nom de son propriétaire.
--
-- Le propriétaire est `created_by`, l'identité AUTHENTIFIÉE de la requête de
-- création — jamais un champ fourni par le client. Le run part à son nom et
-- sous ses droits du moment, revérifiés à chaque déclenchement.
--
-- Aucun DELETE sur cette table, comme sur `users` : `deleted_at` en tient lieu,
-- parce que les runs qu'un cron a lancés le référencent (`run.schedule_id`).
-- Effacer la ligne effacerait la réponse à « pourquoi ce run est-il parti à
-- 3 h du matin ».
--
-- Aucun secret ici : ni mot de passe, ni valeur de `forward_env` (§5.4). Les
-- flags, `-e` compris, sont ceux qui seront écrits en clair dans `run.flags`.

-- +goose Up

CREATE TABLE schedule (
    id   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL
        CONSTRAINT schedule_name_forme CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),

    repo        text NOT NULL CHECK (repo <> ''),
    playbook    text NOT NULL CHECK (playbook <> ''),
    -- Obligatoire, comme pour un lancement depuis le navigateur (ADR-0042).
    environment text NOT NULL
        CONSTRAINT schedule_environment_forme
            CHECK (environment ~ '^[a-z0-9][a-z0-9-]{0,54}$'),
    inventory   text NOT NULL DEFAULT '',
    flags       jsonb NOT NULL DEFAULT '[]'::jsonb
        CONSTRAINT schedule_flags_tableau CHECK (jsonb_typeof(flags) = 'array'),

    cron_expression text NOT NULL CHECK (cron_expression <> ''),
    timezone        text NOT NULL CHECK (timezone <> ''),

    created_by bigint NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,

    enabled      boolean NOT NULL DEFAULT true,
    -- NULL quand le cron est désactivé : il n'a pas d'échéance.
    next_fire_at timestamptz,

    last_fired_at timestamptz,
    last_run_ref  text,
    last_error    text,

    CONSTRAINT schedule_echeance_si_actif
        CHECK (NOT enabled OR deleted_at IS NOT NULL OR next_fire_at IS NOT NULL)
);

-- Un nom par propriétaire, parmi les crons NON supprimés : supprimer puis
-- recréer `nightly-backup` doit rester possible.
CREATE UNIQUE INDEX schedule_nom_par_proprietaire
    ON schedule (created_by, name) WHERE deleted_at IS NULL;

-- La requête du planificateur, à chaque tour et sur chaque réplica.
CREATE INDEX schedule_echus
    ON schedule (next_fire_at) WHERE enabled AND deleted_at IS NULL;

COMMENT ON TABLE schedule IS
    'Runs planifiés (ADR-0057). Le run part au nom de created_by, sous ses droits du moment.';
COMMENT ON COLUMN schedule.last_error IS
    'Refus ou échec du dernier déclenchement, tel que rendu au propriétaire. NULL quand le run est parti.';

-- Le run garde la trace du cron qui l'a lancé. NULL pour un run lancé par une
-- personne : c'est le cas de tous les runs antérieurs.
ALTER TABLE run ADD COLUMN schedule_id bigint REFERENCES schedule (id);

-- GRANT.
--
-- `warden_reader` voit la table : on y lit des noms de dépôt, de playbook et
-- d'environnement déjà visibles sur les runs, et des `-e` déjà en clair dans
-- `run.flags`. `run.schedule_id` est couvert par le SELECT de table existant.
GRANT SELECT ON schedule TO warden_reader;

-- +goose Down

ALTER TABLE run DROP COLUMN IF EXISTS schedule_id;
DROP TABLE IF EXISTS schedule;
