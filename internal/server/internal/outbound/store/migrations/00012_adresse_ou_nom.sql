-- L'adresse d'une machine devient une adresse OU un nom (ADR-0036).
--
-- CE QUE ÇA RENVERSE. La migration 00008 avait choisi `inet` pour interdire un
-- nom d'hôte : « dépendre d'une résolution DNS faite dans le pod ferait enrôler
-- une machine que l'opérateur croyait désigner ». L'intention reste juste, mais
-- le prix s'est révélé en usage — un inventaire Ansible nomme couramment ses
-- machines sans `ansible_host`, et exiger une adresse obligeait à tenir une
-- seconde vérité, à la main, à côté de l'inventaire.
--
-- LE RISQUE N'EST PAS EFFACÉ, IL EST DÉPLACÉ. Un nom est résolu par le
-- résolveur DU POD, dont la vue n'est pas celle du poste de l'opérateur. Ce qui
-- protège de joindre la mauvaise machine reste l'empreinte épinglée
-- (`host_key`), qui elle n'a pas bougé — et qui est la seule chose qui ait
-- jamais protégé de ça.

-- +goose Up

-- `host(address)` dépouille le `/32` que le type `inet` ajoute, exactement
-- comme la lecture le faisait déjà en SQL. Les lignes existantes traversent
-- donc la migration sans changer de valeur apparente.
ALTER TABLE managed_host
    ALTER COLUMN address TYPE text USING host(address);

-- Une contrainte de FORME, volontairement grossière : elle écarte ce qui ne peut
-- être ni une adresse ni un nom — un espace, un guillemet, un saut de ligne, une
-- URL. Le jugement fin appartient à `core.ValiderAdresse`, qui distingue une IP
-- d'un nom et refuse `0.0.0.0` et le multicast. Les deux couches se doublent
-- comme partout ailleurs ici : celle-ci a la base, celle-là peut nommer la
-- machine fautive.
--
-- Le premier caractère accepte `:` pour les adresses IPv6 abrégées (`::1`).
ALTER TABLE managed_host
    ADD CONSTRAINT managed_host_adresse_forme
    CHECK (address ~ '^[A-Za-z0-9:][A-Za-z0-9.:_-]{0,252}$');

COMMENT ON COLUMN managed_host.address IS
    'Où l''enrôlement se connecte : une adresse IP, ou un nom que LE POD sait résoudre (ADR-0036). Ce qui protège de joindre la mauvaise machine est host_key, pas la forme de ce champ.';

-- +goose Down

-- Les lignes dont l'adresse est un NOM ne peuvent pas redevenir des `inet` :
-- les convertir demanderait une résolution, que la base ne fait pas et que
-- personne ne devrait faire à sa place. Elles disparaissent avec la capacité
-- qui les a créées — comme la 00009 supprime ses runs d'enrôlement.
DELETE FROM managed_host
 WHERE address !~ '^[0-9]{1,3}(\.[0-9]{1,3}){3}$'
   AND address !~ ':';

ALTER TABLE managed_host DROP CONSTRAINT IF EXISTS managed_host_adresse_forme;

ALTER TABLE managed_host
    ALTER COLUMN address TYPE inet USING address::inet;

COMMENT ON COLUMN managed_host.address IS
    'Où l''enrôlement se connecte. Une adresse et pas un nom : dépendre du DNS du pod ferait enrôler une machine que l''opérateur croyait désigner.';
