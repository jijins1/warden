-- Configuration des dépôts, et les administrateurs qui la tiennent.
--
-- L'ADR-0027 fait du miroir de la branche protégée l'ANCRE du contrôle de
-- fraîcheur du push, et l'ADR-0028 pose que le serveur seul l'écrit. Pour le
-- rafraîchir, il faut savoir quelle URL de forge et quelle branche protégée
-- correspondent à quel dépôt. Personne ne le sait aujourd'hui : `run.repo` est
-- un nom sans rien derrière, et `RefreshMirror` n'a donc pas d'appelant.
--
-- Ces deux tables comblent exactement ce manque, et rien de plus.
--
-- POURQUOI EN BASE plutôt que dans un fichier de configuration monté : c'est la
-- décision actée n°7 — « le fichier client est une requête ; le serveur détient
-- la politique », leçon Atlantis du §5.4. Une configuration que l'opérateur
-- modifie par redéploiement resterait de la politique serveur, mais elle
-- interdirait d'en tracer les changements, alors que l'URL de forge d'un dépôt
-- décide de ce qui sera exécuté en production.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Administrateurs
--
-- Un booléen et non une table de rôles. Le produit a exactement DEUX niveaux de
-- privilège à ce jour : voir ses propres runs, et administrer la configuration
-- des dépôts. Une table de rôles serait de la généralité spéculative, et le
-- coût de la convertir le jour où un troisième niveau apparaît est un ALTER de
-- plus — bien moindre que celui de porter une indirection dont personne ne se
-- sert.
--
-- AMORÇAGE : le premier administrateur se pose à la main, en SQL, par
-- l'exploitant. Aucun endpoint ne permet de s'accorder ce droit à soi-même, et
-- aucun utilisateur ne naît administrateur — sans quoi le premier à se connecter
-- sur une instance neuve détiendrait la configuration de toute l'organisation.
-- ---------------------------------------------------------------------------
ALTER TABLE users ADD COLUMN is_admin boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN users.is_admin IS
    'Administre la configuration des dépôts. Se pose à la main pour le premier administrateur : aucun endpoint ne permet de se l''accorder, et un utilisateur ne naît jamais avec.';

-- ---------------------------------------------------------------------------
-- repo
--
-- La clé primaire est le NOM, pas un identifiant technique. Ce nom est déjà
-- l'identifiant qui circule partout — `run.repo`, `runner_pod.repo`, le
-- répertoire sous la racine du rôle `git` — et lui ajouter une clé de substitution
-- créerait une seconde façon de désigner un dépôt, donc une occasion de les voir
-- diverger.
--
-- La contrainte de forme est la MÊME que `run_repo_forme` de la migration 00004,
-- et pour la même raison : ce nom devient un composant de chemin sous la racine
-- des dépôts. Le rôle `git` assainit de toute façon avant de joindre, mais une
-- base qui ne peut pas contenir `..` est une défense de moins à oublier.
-- ---------------------------------------------------------------------------
CREATE TABLE repo (
    name text PRIMARY KEY
        CONSTRAINT repo_name_forme
            CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),

    -- URL de la forge. Le rôle `git` la revalide avant tout usage : `ParseRemote`
    -- refuse tout ce qui n'est pas http/https, donc `ext::` qui prend une
    -- commande shell et l'exécute, et `file://` qui atteindrait les dépôts
    -- voisins sur le volume. La contrainte ci-dessous est une seconde barrière,
    -- pas la barrière.
    remote_url text NOT NULL
        CONSTRAINT repo_remote_schema
            CHECK (remote_url ~ '^https?://'),

    -- Branche protégée, en nom de ref COMPLET. Écrire `refs/heads/main` plutôt
    -- que `main` supprime une conversion, et une conversion est un endroit où
    -- deux composants finissent par ne pas s'accorder.
    mainline_ref text NOT NULL
        CONSTRAINT repo_mainline_forme
            CHECK (mainline_ref ~ '^refs/heads/[A-Za-z0-9][A-Za-z0-9._/-]*$'),

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    -- Qui a touché à cette ligne en dernier. La configuration d'un dépôt décide
    -- de ce qui s'exécutera en production : savoir qui l'a changée est du même
    -- ordre que savoir qui a lancé un run.
    updated_by bigint REFERENCES users (id)
);

COMMENT ON TABLE repo IS
    'Politique serveur par dépôt : où est la forge, quelle branche est protégée. Alimente le rafraîchissement du miroir (ADR-0028), qui ancre le contrôle de fraîcheur du push (ADR-0027).';
COMMENT ON COLUMN repo.mainline_ref IS
    'Nom de ref complet, identique côté forge et côté miroir. Une correspondance qui diverge est une question de plus à se poser pendant un incident.';

-- PAS de clé étrangère depuis `run.repo`, et c'est délibéré.
--
-- Deux raisons, la seconde étant la vraie. D'abord les lignes antérieures à la
-- migration 00004 portent la chaîne vide, qui ne référence rien. Ensuite et
-- surtout : les runs vivent indéfiniment (ADR-0013), alors qu'un dépôt peut être
-- retiré de la configuration quand un projet s'arrête. Une clé étrangère
-- forcerait alors à choisir entre garder éternellement une ligne de configuration
-- morte et supprimer des enregistrements d'audit. Un journal d'audit dont les
-- lignes peuvent disparaître à cause d'un ménage de configuration n'est pas un
-- journal d'audit.

GRANT SELECT ON repo TO warden_reader;

-- +goose Down

DROP TABLE IF EXISTS repo;
ALTER TABLE users DROP COLUMN IF EXISTS is_admin;
