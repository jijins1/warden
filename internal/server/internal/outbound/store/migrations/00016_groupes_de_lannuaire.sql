-- La connexion par l'annuaire et les droits par groupe (ADR-0053).
--
-- Jusqu'ici, un droit nommait un compte local (ADR-0033). Un compte AD arrive
-- avec ses GROUPES, et `grants.yaml` accorde désormais des environnements et
-- l'administration à des groupes.
--
-- Rien ne change dans `users` : un compte AD est un `users.type = 'ldap'`, prévu
-- dès le schéma initial, et son sujet est l'objectGUID (ADR-0021).

-- +goose Up

-- ---------------------------------------------------------------------------
-- session_group
--
-- Les groupes que l'annuaire a rendus À LA CONNEXION, figés avec la session. Un
-- droit de groupe vaut tant qu'une session VIVANTE le porte : c'est la durée de
-- la session (dix heures pour un compte AD) qui borne la prise d'effet d'un
-- retrait de groupe dans l'AD, sans interroger l'annuaire à chaque requête.
-- ---------------------------------------------------------------------------

CREATE TABLE session_group (
    session_id bigint NOT NULL REFERENCES session (id),

    -- `sAMAccountName` du groupe, en minuscules : l'AD le compare sans tenir
    -- compte de la casse, et le comparer ici avec la casse donnerait un droit
    -- que personne n'exerce.
    group_name text NOT NULL CHECK (group_name <> '' AND group_name = lower(group_name)),

    PRIMARY KEY (session_id, group_name)
);

-- La question posée à chaque création de run et à chaque livraison d'ordre :
-- « une session vivante de cet utilisateur porte-t-elle un groupe qui a droit ».
-- Elle part des sessions VIVANTES du compte. Les sessions expirées ne sont
-- jamais purgées (ADR-0032), et un compte AD en ouvre une par jour : sans
-- `expires_at` dans l'index, la lecture grossirait avec l'ancienneté du compte.
-- Les groupes de chaque session se lisent ensuite par la clé primaire.
CREATE INDEX session_vivantes_par_compte_idx
    ON session (user_id, expires_at)
    WHERE revoked_at IS NULL;

COMMENT ON TABLE session_group IS
    'Groupes de l''annuaire figés à la connexion (ADR-0053). Leur durée de vie est celle de la session.';

-- ---------------------------------------------------------------------------
-- group_grant
--
-- Le pendant de `environment_grant` pour un groupe, avec les mêmes règles :
-- révocation DATÉE et jamais un DELETE, unicité sur les droits vivants.
--
-- Pas de clé étrangère vers un groupe : un groupe n'est pas provisionné côté
-- warden, il existe dans l'AD.
-- ---------------------------------------------------------------------------

CREATE TABLE group_grant (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    group_name text NOT NULL CHECK (group_name <> '' AND group_name = lower(group_name)),

    environment text NOT NULL
        CONSTRAINT group_grant_environment_forme
            CHECK (environment ~ '^[a-z0-9][a-z0-9-]{0,54}$'),

    granted_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,

    CONSTRAINT group_grant_revoque_apres CHECK (revoked_at IS NULL OR revoked_at >= granted_at)
);

CREATE UNIQUE INDEX group_grant_vivants_idx
    ON group_grant (group_name, environment)
    WHERE revoked_at IS NULL;

COMMENT ON TABLE group_grant IS
    'Droits d''un groupe de l''annuaire sur un environnement (ADR-0053). Même sémantique que environment_grant.';

-- ---------------------------------------------------------------------------
-- admin_group
--
-- Les groupes qui ouvrent l'administration. `users.is_admin` d'un compte AD
-- reste faux : il est CALCULÉ à chaque requête depuis les groupes de la session,
-- pour qu'il n'y ait qu'une source et rien à resynchroniser.
-- ---------------------------------------------------------------------------

CREATE TABLE admin_group (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    group_name text NOT NULL CHECK (group_name <> '' AND group_name = lower(group_name)),

    granted_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,

    CONSTRAINT admin_group_revoque_apres CHECK (revoked_at IS NULL OR revoked_at >= granted_at)
);

CREATE UNIQUE INDEX admin_group_vivants_idx
    ON admin_group (group_name)
    WHERE revoked_at IS NULL;

COMMENT ON TABLE admin_group IS
    'Groupes de l''annuaire qui ouvrent l''administration (ADR-0053). Révocation datée.';

-- GRANT.
--
-- `warden_reader` voit les droits de groupe : « quel groupe avait accès à prod
-- le 3 mars » est une question d'audit, comme pour `environment_grant`.
--
-- RIEN sur `session_group`, comme sur `session` (migration 00010).
--
-- RIEN pour `warden_ingest`.
GRANT SELECT ON group_grant TO warden_reader;
GRANT SELECT ON admin_group TO warden_reader;

-- +goose Down

DROP TABLE IF EXISTS admin_group;
DROP TABLE IF EXISTS group_grant;
DROP TABLE IF EXISTS session_group;
DROP INDEX IF EXISTS session_vivantes_par_compte_idx;
