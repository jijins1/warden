-- Un second genre de run : l'enrôlement d'une machine.
--
-- POURQUOI DANS `run` ET PAS AILLEURS. Un enrôlement pose une clé d'accès sur
-- une machine de production, par une personne nommée, depuis un pod. C'est
-- exactement ce que ce produit existe pour journaliser — et tout ce dont il a
-- besoin est déjà là : le pod, le canal de dispatch, le flux d'events,
-- l'idempotence de l'ingestion, la clôture du §6.4. Une table parallèle aurait
-- dupliqué les six.
--
-- Le prix est celui-ci : six colonnes de `run` ne valent que pour un playbook.
-- Elles cessent d'être `NOT NULL` et deviennent conditionnelles au genre, ce qui
-- est plus fidèle que ce qui existait — elles n'étaient obligatoires que parce
-- qu'aucun autre genre n'existait.

-- +goose Up

-- `playbook` par défaut : toutes les lignes existantes en sont, et la valeur par
-- défaut évite d'avoir à les réécrire.
ALTER TABLE run ADD COLUMN kind text NOT NULL DEFAULT 'playbook'
    CONSTRAINT run_kind CHECK (kind IN ('playbook', 'enrollment'));

-- La cible de l'enrôlement : nom, adresse, port, compte.
--
-- Un JSONB et non quatre colonnes, parce qu'elles ne serviraient qu'à un genre
-- sur deux et resteraient vides sur toute la table. Ce n'est PAS une donnée
-- qu'on requête : elle est lue une fois, à la livraison de l'ordre.
--
-- Elle ne contient JAMAIS le mot de passe. Celui-ci vient d'un humain, ne sert
-- qu'une fois, et n'a rien à faire dans un journal répliqué, sauvegardé et
-- jamais purgé (ADR-0013). Il transite par un Secret Kubernetes éphémère, lu à
-- la livraison et détruit ensuite.
ALTER TABLE run ADD COLUMN enroll_target jsonb;

COMMENT ON COLUMN run.enroll_target IS
    'Cible d''un run d''enrôlement : host, address, port, login. JAMAIS le mot de passe, qui transite par un Secret éphémère.';

-- Les six colonnes qui ne valent que pour un playbook.
--
-- `inventory` et `playbook` décrivent ce qu'on exécute ; les quatre SHA et refs
-- décrivent l'artefact et son point de comparaison. Un enrôlement n'a ni l'un ni
-- l'autre : il n'exécute pas de code de l'utilisateur et ne se compare à rien.
ALTER TABLE run ALTER COLUMN inventory    DROP NOT NULL;
ALTER TABLE run ALTER COLUMN playbook     DROP NOT NULL;
ALTER TABLE run ALTER COLUMN audit_sha    DROP NOT NULL;
ALTER TABLE run ALTER COLUMN base_sha     DROP NOT NULL;
ALTER TABLE run ALTER COLUMN mainline_ref DROP NOT NULL;
ALTER TABLE run ALTER COLUMN mainline_sha DROP NOT NULL;

-- Ce que `NOT NULL` disait, dit maintenant par genre.
--
-- La contrainte est ce qui empêche la relaxation ci-dessus de dégénérer en
-- « tout est facultatif » : un run de playbook sans commit d'audit reste
-- impossible, et c'est le seul point qui comptait.
ALTER TABLE run ADD CONSTRAINT run_playbook_complet CHECK (
    kind <> 'playbook' OR (
        inventory    IS NOT NULL AND
        playbook     IS NOT NULL AND
        audit_sha    IS NOT NULL AND
        base_sha     IS NOT NULL AND
        mainline_ref IS NOT NULL AND
        mainline_sha IS NOT NULL
    )
);

-- Et symétriquement : un enrôlement SANS cible ne saurait pas quoi joindre, et
-- un playbook AVEC une cible d'enrôlement décrirait deux choses à la fois.
ALTER TABLE run ADD CONSTRAINT run_enrolement_cible CHECK (
    (kind = 'enrollment') = (enroll_target IS NOT NULL)
);

COMMENT ON COLUMN run.kind IS
    'playbook : exécute le commit d''audit. enrollment : pose la clé d''accès sur une machine. Les deux sont des runs parce que les deux sont des actions auditées sur la production.';

-- +goose Down

ALTER TABLE run DROP CONSTRAINT IF EXISTS run_enrolement_cible;
ALTER TABLE run DROP CONSTRAINT IF EXISTS run_playbook_complet;

-- Les lignes d'enrôlement portent des NULL que les `NOT NULL` restaurés
-- refuseraient : elles disparaissent avec le genre qui les a créées.
DELETE FROM run WHERE kind = 'enrollment';

ALTER TABLE run ALTER COLUMN inventory    SET NOT NULL;
ALTER TABLE run ALTER COLUMN playbook     SET NOT NULL;
ALTER TABLE run ALTER COLUMN audit_sha    SET NOT NULL;
ALTER TABLE run ALTER COLUMN base_sha     SET NOT NULL;
ALTER TABLE run ALTER COLUMN mainline_ref SET NOT NULL;
ALTER TABLE run ALTER COLUMN mainline_sha SET NOT NULL;

ALTER TABLE run DROP COLUMN IF EXISTS enroll_target;
ALTER TABLE run DROP COLUMN IF EXISTS kind;
