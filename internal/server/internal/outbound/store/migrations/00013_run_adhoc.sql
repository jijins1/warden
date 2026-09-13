-- Un troisième genre de run : l'ad-hoc, sans playbook.
--
-- POURQUOI DANS `run` ET PAS AILLEURS. Un run ad-hoc exécute un module Ansible
-- contre un motif de machines, sans playbook — l'équivalent de la commande
-- `ansible` par opposition à `ansible-playbook`. C'est exactement le même
-- raisonnement que celui de la migration 00009 pour l'enrôlement : le pod, le
-- canal de dispatch, le flux d'events, l'idempotence de l'ingestion, la
-- clôture du §6.4 sont déjà là et n'ont rien de spécifique au playbook.
--
-- Contrairement à l'enrôlement, un ad-hoc reste plus proche du playbook qu'il
-- ne s'en éloigne : il a toujours besoin du dépôt audité, de l'inventaire et
-- des quatre SHA/refs qui décrivent l'artefact. Seule la colonne `playbook`
-- devient facultative — remplacée par une cible ad-hoc.

-- +goose Up

ALTER TABLE run DROP CONSTRAINT run_kind;
ALTER TABLE run ADD CONSTRAINT run_kind CHECK (kind IN ('playbook', 'enrollment', 'adhoc'));

-- Le module, ses arguments, et le motif de machines. Un JSONB et non trois
-- colonnes, pour la même raison qu'`enroll_target` : elles ne servent qu'à un
-- genre de run sur trois et resteraient vides sur toute la table.
ALTER TABLE run ADD COLUMN adhoc_target jsonb;

COMMENT ON COLUMN run.adhoc_target IS
    'Cible d''un run ad-hoc : module, arguments, motif de machines. Symétrique d''enroll_target.';

-- `playbook` cesse d'être obligatoire pour tout ce qui n'est pas un playbook.
-- Il l'était déjà pour un enrôlement (migration 00009) ; l'ad-hoc s'ajoute au
-- même régime.
ALTER TABLE run ADD CONSTRAINT run_adhoc_complet CHECK (
    kind <> 'adhoc' OR (
        inventory    IS NOT NULL AND
        audit_sha    IS NOT NULL AND
        base_sha     IS NOT NULL AND
        mainline_ref IS NOT NULL AND
        mainline_sha IS NOT NULL AND
        adhoc_target IS NOT NULL
    )
);

-- Symétriquement : un ad-hoc SANS cible ne saurait pas quoi exécuter, et un
-- playbook ou un enrôlement AVEC une cible ad-hoc décrirait deux choses à la
-- fois.
ALTER TABLE run ADD CONSTRAINT run_adhoc_cible CHECK (
    (kind = 'adhoc') = (adhoc_target IS NOT NULL)
);

-- Un ad-hoc n'a pas de playbook, par construction : la colonne existe pour
-- `playbook` et `enrollment`, jamais les deux à la fois avec une cible ad-hoc.
ALTER TABLE run ADD CONSTRAINT run_adhoc_sans_playbook CHECK (
    kind <> 'adhoc' OR playbook IS NULL
);

COMMENT ON COLUMN run.kind IS
    'playbook : exécute le commit d''audit. enrollment : pose la clé d''accès sur une machine. adhoc : exécute un module isolé, sans playbook. Les trois sont des runs parce que les trois sont des actions auditées sur la production.';

-- +goose Down

ALTER TABLE run DROP CONSTRAINT IF EXISTS run_adhoc_sans_playbook;
ALTER TABLE run DROP CONSTRAINT IF EXISTS run_adhoc_cible;
ALTER TABLE run DROP CONSTRAINT IF EXISTS run_adhoc_complet;

-- Les lignes ad-hoc portent des NULL (`playbook`) que le genre qui les a
-- créées est seul à justifier : elles disparaissent avec lui.
DELETE FROM run WHERE kind = 'adhoc';

ALTER TABLE run DROP COLUMN IF EXISTS adhoc_target;

ALTER TABLE run DROP CONSTRAINT run_kind;
ALTER TABLE run ADD CONSTRAINT run_kind CHECK (kind IN ('playbook', 'enrollment'));
