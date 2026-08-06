-- Schéma initial : utilisateurs, runs, events, diffs (ADR-0021).
--
-- Trois contraintes du produit se lisent directement dans ce fichier :
--   - l'ADR-0013 ne purge rien, donc tout ce qui entre ici y reste ;
--   - l'ADR-0008 fait de cette base le journal ET le transport des events ;
--   - l'ADR-0021 sépare une enveloppe à colonnes d'un payload opaque.
--
-- +goose Up

-- ---------------------------------------------------------------------------
-- Rôles.
--
-- Créés ici en NOLOGIN — ce sont des rôles de groupe, que l'opérateur du
-- cluster attribue aux utilisateurs de connexion réels. Les GRANT font partie
-- des migrations (ADR-0012) : ils sont versionnés avec le schéma qu'ils
-- restreignent, donc revus au même endroit.
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'warden_ingest') THEN
        CREATE ROLE warden_ingest NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'warden_reader') THEN
        CREATE ROLE warden_reader NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'warden_diff_reader') THEN
        CREATE ROLE warden_diff_reader NOLOGIN;
    END IF;
END
$$;
-- +goose StatementEnd

-- ---------------------------------------------------------------------------
-- users
--
-- Provisionnés à la volée à la première arrivée (ADR-0021). La clé naturelle
-- est l'identifiant IMMUABLE du fournisseur — `sub` OIDC, `entryUUID` ou
-- `objectGUID` LDAP — jamais l'email ni le login, qui changent. Un run de 2026
-- doit encore se résoudre à la bonne personne en 2032.
--
-- Aucun DELETE sur cette table, jamais : `disabled_at` est là pour ça.
-- ---------------------------------------------------------------------------

CREATE TABLE users (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    type         text        NOT NULL CHECK (type IN ('oidc', 'ldap', 'local')),
    subject      text        NOT NULL CHECK (subject <> ''),
    display_name text        NOT NULL DEFAULT '',
    email        text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    disabled_at  timestamptz,

    UNIQUE (type, subject)
);

COMMENT ON COLUMN users.subject IS
    'Identifiant immuable du fournisseur. Jamais l''email ni le login (ADR-0021).';
COMMENT ON COLUMN users.display_name IS
    'Instantané rafraîchi à la connexion : l''audit affiche l''identité actuelle d''un acteur passé.';
COMMENT ON COLUMN users.disabled_at IS
    'Désactivation. Aucun DELETE sur cette table : les runs vivent indéfiniment (ADR-0013).';

-- ---------------------------------------------------------------------------
-- run
--
-- `id` est un bigint et non un UUID : la table d'events le porte en clé, et
-- 8 octets contre 16 se paient sur la table qui grossit le plus et ne se purge
-- jamais. `run_ref` est un ULID, sûr comme nom de ref git (ADR-0007) et dans
-- une URL ; c'est lui qui sort du produit.
--
-- Un `id` séquentiel est devinable, et l'ingestion reçoit (run_id, seq) d'un
-- pod hostile. C'est sans conséquence PARCE QUE le lien run_id <-> pod est
-- établi côté serveur et jamais accepté depuis le corps d'une requête. Ce n'est
-- pas un détail d'implémentation : c'est une condition de validité du schéma.
-- ---------------------------------------------------------------------------

CREATE TABLE run (
    id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_ref text NOT NULL UNIQUE CHECK (run_ref ~ '^[0-9A-HJKMNP-TV-Z]{26}$'),
    user_id bigint NOT NULL REFERENCES users (id),

    -- Clé de partition des events de ce run. Voir la table `event`. Jamais
    -- fournie par l'application : la valeur par défaut est la seule source, et
    -- elle est ancrée en UTC pour ne pas dépendre du fuseau de la session.
    run_day date NOT NULL DEFAULT (timezone('UTC', now()))::date,

    environment text NOT NULL,
    inventory   text NOT NULL,
    playbook    text NOT NULL,

    -- Ce que git ne conserve pas (ADR-0021). Le contenu à un SHA est figé par
    -- git ; la position d'une branche à une date ne l'est pas — les reflogs
    -- expirent et sont locaux au miroir.
    audit_sha    text NOT NULL CHECK (audit_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    base_sha     text NOT NULL CHECK (base_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    mainline_ref text NOT NULL,
    mainline_sha text NOT NULL CHECK (mainline_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),

    -- Flags tels qu'exécutés (§5.5). Aucune colonne « demandé » en face : la
    -- politique serveur est mise de côté, donc demandé et effectif coïncident
    -- (ADR-0021). Le jour où elle existera, il faudra ajouter la trace de ce
    -- qui a été filtré.
    flags jsonb NOT NULL DEFAULT '{}'::jsonb,

    state             text NOT NULL CHECK (state IN ('running', 'succeeded', 'failed', 'interrupted')),
    termination_cause text CHECK (termination_cause IN (
        'user_interrupt', 'client_disconnect_timeout', 'server_task_timeout',
        'pod_oom_killed', 'pod_evicted', 'node_lost', 'admin_killed', 'unknown')),

    runner_image        text,
    runner_image_digest text,
    runner_pod          text,

    changed_count    integer NOT NULL DEFAULT 0 CHECK (changed_count >= 0),
    secrets_unmasked boolean NOT NULL DEFAULT false,
    secrets_unmasked_reason text,

    started_at timestamptz NOT NULL DEFAULT now(),
    ended_at   timestamptz,

    artifact_pruned_at timestamptz,

    -- « Interrompu » sans la cause est presque inutile (§6.4), et une cause
    -- sur un run réussi n'a pas de sens.
    CONSTRAINT run_cause_iff_interrupted
        CHECK ((state = 'interrupted') = (termination_cause IS NOT NULL)),
    CONSTRAINT run_unmask_reason_iff_unmasked
        CHECK (secrets_unmasked = (secrets_unmasked_reason IS NOT NULL))
);

COMMENT ON COLUMN run.state IS
    'Statut initial ''interrupted''/''unknown'', écrit AVANT la création du pod (§6.4) : un plantage entre l''insertion et le dispatch laisse une ligne honnête.';
COMMENT ON COLUMN run.mainline_sha IS
    'Où pointait la branche de référence à cet instant. Sans elle, le diff « run contre branche principale » du §6.5 se recalcule contre la branche d''aujourd''hui, donc change à chaque consultation.';
COMMENT ON COLUMN run.changed_count IS
    'Dénormalisé (ADR-0008) : métrique d''audit principale, ne doit pas coûter une agrégation. Maintenu par le trigger event_bump_changed.';
COMMENT ON COLUMN run.artifact_pruned_at IS
    'ADR-0013 : aucune purge à ce jour. Présente dès la première migration pour que l''interface d''audit traite le cas « code purgé » tout de suite, et non le jour de la première purge.';
COMMENT ON COLUMN run.run_day IS
    'Clé de partition des events du run. Immuable : la déplacer déplacerait les events.';

-- La liste filtrable du §6.5, et le §6.6 « le dernier run sur cet inventaire ».
CREATE INDEX run_started_at_idx ON run (started_at DESC);
CREATE INDEX run_env_inventory_idx ON run (environment, inventory, started_at DESC);
CREATE INDEX run_user_idx ON run (user_id, started_at DESC);

-- ---------------------------------------------------------------------------
-- event
--
-- Enveloppe normalisée à colonnes + payload opaque (ADR-0021). Le critère de
-- partage : l'enveloppe contient ce qui existe pour TOUT event, quel que soit
-- le module et quelle que soit la version d'ansible-core. C'est pourquoi
-- stdout et stderr ne sont pas des colonnes — ils n'existent que pour command,
-- shell, raw et script.
--
-- Partitionnée par `run_day` et NON par la date de l'event. PostgreSQL exige
-- que la clé de partition fasse partie de toute contrainte d'unicité : une
-- partition sur `occurred_at` aurait donc rendu l'unicité (run_id, seq, date),
-- et deux insertions du même (run_id, seq) avec un horodatage différent
-- auraient toutes deux été acceptées. L'idempotence de l'ADR-0008 en dépend.
--
-- `run_day` étant une fonction de `run_id` — recopiée depuis la ligne de run,
-- jamais fournie par le runner — l'unicité (run_id, run_day, seq) équivaut
-- exactement à l'unicité (run_id, seq). Effet de bord utile : les events d'un
-- run ne sont jamais à cheval sur deux partitions.
-- ---------------------------------------------------------------------------

CREATE TABLE event (
    run_id  bigint NOT NULL REFERENCES run (id),
    run_day date   NOT NULL,
    seq     bigint NOT NULL CHECK (seq >= 0),

    occurred_at timestamptz NOT NULL,

    host        text,
    task_number integer,
    task_name   text,
    role        text,

    -- 'unknown' n'est pas une lacune : c'est la soupape qui rend acceptable
    -- qu'un traducteur soit en retard sur une version d'ansible-core déployée
    -- par un projet (ADR-0014, ADR-0021). L'event est alors stocké tel quel,
    -- payload d'origine intact. Jamais ignoré, jamais perdu.
    state text NOT NULL CHECK (state IN (
        'ok', 'changed', 'failed', 'skipped', 'unreachable', 'unknown')),

    payload jsonb NOT NULL,

    PRIMARY KEY (run_id, run_day, seq)
) PARTITION BY RANGE (run_day);

COMMENT ON TABLE event IS
    'Journal d''audit et transport du streaming (ADR-0008). Aucune purge (ADR-0013).';
COMMENT ON COLUMN event.seq IS
    'Offset assigné par le RUNNER, monotone par run : c''est ce qui rend l''insertion idempotente et l''ingestion parallélisable sans contrainte d''ordre (ADR-0008).';
COMMENT ON COLUMN event.payload IS
    'Résultat de module opaque, stdout et stderr compris. La redaction de l''ADR-0017 s''applique à TOUT le payload, pas aux seuls diffs.';

-- « Tout ce qui a touché db-prod-02 ce mois-ci » : la question la plus
-- fréquente en incident (§6.5), et un index ajouté après coup sur une table
-- d'events est une opération pénible. Partiel — un event de niveau playbook
-- n'a pas d'hôte, et il ne représente rien dans cette vue.
CREATE INDEX event_host_idx ON event (host, run_day DESC) WHERE host IS NOT NULL;

-- ---------------------------------------------------------------------------
-- event_diff
--
-- Table séparée à contrôle d'accès distinct (ADR-0008) : les payloads --diff
-- contiennent les secrets rendus (§6.5), et les mélanger aux events ordinaires
-- rendrait impossible d'ouvrir le journal sans ouvrir les secrets.
--
-- Ce n'est pas la seule table à en contenir — un `shell: mysql -p{{ password }}`
-- met le secret dans un event ordinaire. C'est celle qui en contient le plus
-- souvent.
-- ---------------------------------------------------------------------------

CREATE TABLE event_diff (
    run_id  bigint NOT NULL,
    run_day date   NOT NULL,
    seq     bigint NOT NULL,

    diff jsonb NOT NULL,

    PRIMARY KEY (run_id, run_day, seq),
    FOREIGN KEY (run_id, run_day, seq) REFERENCES event (run_id, run_day, seq)
) PARTITION BY RANGE (run_day);

-- ---------------------------------------------------------------------------
-- Compteur `changed` dénormalisé.
--
-- Trigger STATEMENT avec table de transition : une seule UPDATE par lot inséré,
-- et non une par event. SECURITY DEFINER pour que le rôle d'ingestion n'ait pas
-- besoin d'UPDATE sur `run` — le compteur ne peut donc pas dériver, et le
-- GRANT reste au plus juste.
--
-- Les lignes écartées par ON CONFLICT DO NOTHING n'entrent pas dans la table de
-- transition : une réémission ne double pas le compteur.
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE FUNCTION warden_bump_changed() RETURNS trigger
    LANGUAGE plpgsql
    SECURITY DEFINER
    SET search_path = pg_catalog, public
AS $$
BEGIN
    UPDATE run r
       SET changed_count = r.changed_count + c.n
      FROM (
            SELECT run_id, count(*) AS n
              FROM new_events
             WHERE state = 'changed'
             GROUP BY run_id
           ) c
     WHERE r.id = c.run_id;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER event_bump_changed
    AFTER INSERT ON event
    REFERENCING NEW TABLE AS new_events
    FOR EACH STATEMENT
    EXECUTE FUNCTION warden_bump_changed();

-- ---------------------------------------------------------------------------
-- Partitions.
--
-- Le partitionnement n'est PAS une migration (ADR-0012) : créer la partition
-- de la période suivante est une tâche récurrente, portée par la maintenance
-- planifiée du rôle `git`. Cette fonction est cette logique ; la migration ne
-- fait qu'amorcer le mois courant.
--
-- Granularité mensuelle : un run produit des milliers d'events, pas des
-- millions, et une dizaine de runs par jour donne ~10^5 lignes par mois. Des
-- partitions quotidiennes seraient 365 objets par an pour rien. La propriété
-- qui compte — pouvoir détacher une plage ancienne, repli prévu par
-- l'ADR-0008 — tient dans les deux cas.
--
-- AUCUNE partition DEFAULT, volontairement. Elle transformerait « les runs
-- s'arrêtent », qui est bruyant et se corrige en une commande, en « des lignes
-- s'accumulent au mauvais endroit », qui est silencieux et interdit ensuite de
-- créer la partition correcte. L'ADR-0012 range l'absence de partition
-- utilisable au rang d'une indisponibilité de la base : c'est une alerte de
-- premier niveau, pas un cas à absorber.
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE FUNCTION warden_ensure_partitions(months_ahead integer DEFAULT 2)
    RETURNS TABLE (created text)
    LANGUAGE plpgsql
AS $$
DECLARE
    parent  text;
    m       integer;
    lo      date;
    hi      date;
    part    text;
BEGIN
    IF months_ahead < 0 THEN
        RAISE EXCEPTION 'months_ahead doit être positif, reçu %', months_ahead;
    END IF;

    FOREACH parent IN ARRAY ARRAY['event', 'event_diff'] LOOP
        FOR m IN 0..months_ahead LOOP
            lo   := (date_trunc('month', timezone('UTC', now())) + (m || ' months')::interval)::date;
            hi   := (lo + interval '1 month')::date;
            part := format('%s_%s', parent, to_char(lo, 'YYYYMM'));

            IF to_regclass(quote_ident(part)) IS NULL THEN
                EXECUTE format(
                    'CREATE TABLE %I PARTITION OF %I FOR VALUES FROM (%L) TO (%L)',
                    part, parent, lo, hi);
                created := part;
                RETURN NEXT;
            END IF;
        END LOOP;
    END LOOP;
END
$$;
-- +goose StatementEnd

COMMENT ON FUNCTION warden_ensure_partitions IS
    'Crée les partitions manquantes du mois courant et des N suivants. Idempotente. Appelée par la maintenance planifiée du rôle `git` (ADR-0012), jamais sur le chemin d''insertion.';

SELECT warden_ensure_partitions(2);

-- ---------------------------------------------------------------------------
-- GRANT.
--
-- Les privilèges sont vérifiés sur la table nommée dans la requête : le
-- routage vers une partition n'exige rien sur la partition. Les partitions
-- créées plus tard par warden_ensure_partitions n'ont donc pas de GRANT à
-- recevoir.
-- ---------------------------------------------------------------------------

-- Ingestion : INSERT, et rien d'autre (ADR-0008). Le SELECT sur `run` est
-- limité à deux colonnes — l'existence du run et la partition où écrire ses
-- events. Sans lui, `run_day` viendrait du corps de la requête, donc du pod
-- hostile, et une valeur choisie ferait écrire un run dans la partition d'un
-- autre mois.
GRANT INSERT               ON event, event_diff TO warden_ingest;
GRANT SELECT (id, run_day) ON run               TO warden_ingest;

-- Lecture d'audit SANS les diffs : c'est ce qui donne son sens à la séparation
-- de la table event_diff.
GRANT SELECT ON users, run, event TO warden_reader;
GRANT SELECT ON event_diff        TO warden_diff_reader;

-- +goose Down

DROP TRIGGER IF EXISTS event_bump_changed ON event;
DROP FUNCTION IF EXISTS warden_bump_changed();
DROP FUNCTION IF EXISTS warden_ensure_partitions(integer);
DROP TABLE IF EXISTS event_diff;
DROP TABLE IF EXISTS event;
DROP TABLE IF EXISTS run;
DROP TABLE IF EXISTS users;

-- Les rôles ne sont pas supprimés : ils peuvent être attribués à des rôles de
-- connexion créés par l'opérateur du cluster, hors de portée de cette
-- migration.
