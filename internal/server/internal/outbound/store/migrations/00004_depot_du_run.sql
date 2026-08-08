-- Dépôt du run.
--
-- Le schéma initial dit d'un run son environnement, son inventaire et son
-- playbook, mais pas de QUEL DÉPÔT il vient. C'est une omission, pas un choix :
-- `runner_pod` porte déjà `repo` depuis l'ADR-0023, et sa clé de pool est
-- (user_id, repo, environment).
--
-- Elle bloque le §6.5. L'ADR-0006 pose que « tous les runs d'un dépôt vivent
-- dans le même bare repo » — c'est ce qui rend possibles la déduplication par
-- blobs et les diffs d'audit — et le rôle `git` détient un bare repo par dépôt.
-- Sans cette colonne, il ne peut pas savoir lequel ouvrir pour un run donné, et
-- les deux diffs qui justifient le choix de git restent inaccessibles.
--
-- Contrairement à `environment`, `inventory` et `playbook`, cette colonne porte
-- une contrainte de format. La différence n'est pas un caprice de cohérence :
-- les trois autres ne sont que des étiquettes, alors que celle-ci devient un
-- COMPOSANT DE CHEMIN sous la racine des dépôts du rôle `git`. Le rôle `git`
-- assainit de toute façon avant de joindre — c'est lui qui décide ce qu'est un
-- nom valide — mais une base qui ne peut pas contenir `..` est une défense de
-- moins à ne jamais oublier.
--
-- Les GRANT ne bougent pas : `warden_reader` a un SELECT au niveau table, qui
-- couvre les colonnes ajoutées ensuite, et `warden_ingest` a un SELECT limité
-- à (id, run_day) que cette colonne ne concerne pas.

-- +goose Up

-- DEFAULT le temps de l'ALTER, puis retiré : les lignes antérieures à cette
-- migration n'ont pas de dépôt à déclarer et gardent la chaîne vide, tandis que
-- toute insertion ultérieure doit nommer le sien. Laisser le DEFAULT en place
-- rendrait l'oubli silencieux et indistinguable de l'héritage.
ALTER TABLE run ADD COLUMN repo text NOT NULL DEFAULT ''
    CONSTRAINT run_repo_forme
        CHECK (repo = '' OR repo ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$');

ALTER TABLE run ALTER COLUMN repo DROP DEFAULT;

COMMENT ON COLUMN run.repo IS
    'Dépôt d''où vient le run. Le rôle `git` en déduit le bare repo à ouvrir (ADR-0006) : sans lui, les diffs du §6.5 sont incalculables. La chaîne vide marque une ligne antérieure à la migration 00004 ; son artefact n''est pas localisable et l''audit répond 410, comme pour un artefact purgé (ADR-0013). Doit dériver EXACTEMENT comme runner_pod.repo : deux dérivations divergentes feraient servir le mauvais dépôt par un pod chaud.';

-- Index du « run précédent sur le même inventaire » (§6.5).
--
-- `run_env_inventory_idx (environment, inventory, started_at DESC)` ne convient
-- PAS : le dépôt est en tête du prédicat et n'est pas un préfixe de cet index.
-- Il doit y être, et pas seulement pour la performance — deux runs d'inventaires
-- homonymes dans des dépôts différents n'ont aucun ancêtre commun, et les
-- differ ne produirait pas un mauvais résultat mais un échec de git.
CREATE INDEX run_repo_env_inventory_idx
    ON run (repo, environment, inventory, started_at DESC);

-- +goose Down

DROP INDEX IF EXISTS run_repo_env_inventory_idx;
ALTER TABLE run DROP COLUMN repo;
