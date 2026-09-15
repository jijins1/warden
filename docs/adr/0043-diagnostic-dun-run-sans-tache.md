# ADR-0043 — Dire pourquoi un run n'a rien exécuté

**Date** : 2026-09-15
**Statut** : Accepted
**S'appuie sur** : [ADR-0011](0011-un-binaire-par-domaine-de-confiance.md), [ADR-0017](0017-redaction-des-diffs-par-substitution-exacte.md), [ADR-0021](0021-modele-devent-normalise.md)

## Contexte

Un run de production a échoué en visant un playbook qui n'existait pas.
`warden logs` a affiché exactement ceci, et rien d'autre :

```
run 01M2E1RVJGRPST6C3F7Z8S51BJ : failed
```

L'interface n'en disait pas plus. Il a fallu deviner la cause et relancer à
l'aveugle pour la confirmer — et comme le contrôleur ramasse les pods terminés
en une minute (§8.7), les journaux du conteneur avaient déjà disparu.

La cause est structurelle, et elle tient en trois faits :

1. Le modèle d'events est **entièrement par tâche et par hôte**. Un échec qui
   survient avant la première tâche ne produit donc aucun event « utile ».
2. `core.TerminationCause` — le seul champ qui explique une fin de run — est
   **rejeté par la validation** quand l'état vaut `failed` : il n'existe que
   pour `interrupted`, avec un vocabulaire fermé.
3. En conséquence, `render.Issue` n'avait plus qu'un statut nu à imprimer, et
   `RunDetail` n'a aucun champ de texte libre à afficher.

Le produit ne pouvait donc pas dire pourquoi, où que ce soit.

## Ce qui a été MESURÉ

La décision repose sur des mesures faites dans l'image du runner, pas sur des
suppositions — et deux d'entre elles ont corrigé la conception en cours de
route :

- **`stderr` est VIDE.** Un playbook introuvable ne laisse pas un octet sur la
  sortie d'erreur. La conception initiale — transporter `stderr`, déjà capturé
  et jeté par `borne` — n'aurait donc **rien** produit.
- Le message vit dans le flux JSONL, en trame `verbose` :
  `{"event":"verbose","stdout":"ERROR! the playbook: x.yml could not be found"}`.
- **Ces trames étaient déjà persistées.** `etat()` les range dans la soupape
  `unknown` de l'ADR-0021 (« stocké tel quel, jamais ignoré, jamais perdu »).
  Le message était donc en base depuis le début : le défaut était un défaut
  d'AFFICHAGE, pas de capture.
- **Un run qui RÉUSSIT émet déjà cinq trames `verbose`** — le seul
  avertissement de découverte d'interpréteur Python, qu'Ansible découpe en cinq
  fragments. C'est la mesure qui interdit la solution évidente.

## Ce qui est décidé

### 1. Un event, et non une colonne de clôture

Le diagnostic voyage comme un event ordinaire plutôt que comme un champ de la
clôture du run. Ce que ce choix achète :

- **Le masquage des secrets est hérité.** `Reporter.masquer()` applique déjà
  `redact.RedactRaw` au payload de chaque event, avec la politique fail-closed
  de l'ADR-0017. Un message qui emprunte ce chemin est masqué sans une ligne de
  code de plus.
- Ni migration, ni `GRANT` à étendre, ni modification de `Final`/`Closure` et de
  leurs validations — lesquelles refusent précisément toute cause sur un run
  `failed`.
- L'event est persisté, donc relisible longtemps après le ramassage du pod, ce
  qui est le besoin réel.

### 2. Une clé que seul warden écrit : `warden_error`

Dé-filtrer les events sans hôte aurait fait remonter les cinq trames `verbose`
d'un run qui réussit — exactement le bruit que le filtre existant écarte. Le
diagnostic porte donc une clé de payload que seul warden pose, sur le modèle du
`warden_flux` déjà émis sur un flux tronqué. L'affichage montre CETTE clé, et
rien d'autre.

La constante vit dans `internal/events`, c'est-à-dire dans le socle partagé :
le runner l'écrit, la CLI la lit, et l'ADR-0011 interdit à la seconde d'importer
le premier. L'ajout au socle est d'une ligne, ce que le même ADR demande de
peser puisqu'il grossit aussi l'image hostile.

### 3. Deux sources, dans cet ordre

1. **Pré-contrôle**, avant tout lancement : le playbook et l'inventaire
   existent-ils dans l'arbre matérialisé ? Le message est alors **écrit par
   warden**, nomme le chemin fautif, et aucun texte hostile ne circule. Il
   couvre l'incident d'origine et évite de payer le démarrage d'un interpréteur
   pour apprendre qu'un fichier manque.
2. **Repli**, si le run échoue **et** qu'aucune tâche n'a tourné : les textes
   `verbose` collectés sont joints en un message. Il couvre ce que warden ne
   sait pas vérifier lui-même — erreur de syntaxe, variable indéfinie — et rend
   en pratique le fichier, la ligne et la colonne fautifs.

La condition du repli est étroite — `failed` **et** zéro tâche — et ne peut donc
pas se déclencher sur un run ordinaire.

### 4. Le premier assainisseur sortant du dépôt

Ce message atteint un terminal (`warden logs`) et un navigateur, et il peut
reprendre la sortie d'`ansible-runner`, qui tourne dans le domaine hostile de
l'ADR-0011. Le dépôt n'avait **aucun** assainisseur de sortie : le seul filtre
de caractères de contrôle existant (`verifierExtraVar`) garde une valeur *hors*
du système.

Trois bornes, appliquées à la source :

- les séquences ANSI sont retirées **entières** — ôter le seul `ESC` laisserait
  un « [31m » bien visible ;
- les caractères de contrôle restants deviennent des espaces, jamais rien : un
  retour à la ligne qui s'évapore recolle deux mots ;
- la longueur est bornée, et la troncature est **visible**, contrairement à
  celle de `borne`.

Le navigateur n'interpole jamais par `innerHTML` — la règle est déjà écrite dans
les gabarits concernés — donc le risque résiduel est le terminal, et c'est lui
que l'assainisseur ferme.

## Solutions envisagées

### Option A — Une colonne `failure_reason` sur `run` ❌ rejetée
- **Rejetée parce que** : il aurait fallu une migration, l'extension du `GRANT`
  de `warden_ingest`, la relaxation de validations qui refusent aujourd'hui
  toute cause sur un run `failed` — et surtout **réimplémenter le masquage**,
  puisque le champ n'aurait pas traversé `Reporter.masquer()`. Le seul
  précédent de texte libre du runner persisté (`secrets_unmasked_reason`) n'est
  d'ailleurs jamais affiché.

### Option B — Dé-filtrer les events sans hôte ❌ rejetée
- **Rejetée parce que** : MESURÉ, un run qui réussit en émet cinq. C'est la
  solution qui rend le bruit permanent pour rendre le diagnostic occasionnel.

### Option C — Transporter `stderr` ❌ rejetée
- **Rejetée parce que** : MESURÉ, il est vide dans ce cas. Elle n'aurait rien
  affiché du tout, ce qui est le défaut qu'on corrige.

### Option D — Un event portant `warden_error`, deux sources, assaini ✅ (retenue)

## Conséquences

- Un échec pré-exécution est désormais expliqué dans `warden logs` comme dans
  l'interface, et l'explication survit au ramassage du pod.
- L'event synthétique `warden_flux` — émis sur un flux tronqué, et jusqu'ici
  invisible pour exactement la même raison — reste invisible : il porte une
  autre clé. Le rendre visible est un choix distinct, à faire sciemment.
- Les tests d'étage 1 du pilote posent maintenant RÉELLEMENT le playbook et
  l'inventaire dans leur arbre temporaire. Ils décrivaient un état que la
  production n'atteint jamais, puisque l'arbre est matérialisé depuis le commit
  audité.
- Le pré-contrôle ajoute deux `os.Stat` par run. C'est négligeable devant le
  démarrage d'un interpréteur Python, qu'il évite justement quand il mord.

## Ce que ça n'achète pas

- **Le masquage reste faillible là où l'ADR-0017 l'admet** : un secret de moins
  de cinq caractères, ou dépassant le seuil de correspondances, traverse en
  clair et le run est signalé par `secrets_unmasked`. C'est déjà vrai de tous
  les payloads d'events ; ce diagnostic ne fait ni mieux ni pire.
- **Aucune classification** : le message est rendu tel qu'Ansible l'a écrit,
  sans être rangé dans un vocabulaire fermé. Un jour où l'on voudra compter les
  causes d'échec, il faudra autre chose que du texte libre.
- **Rien pour les runs interrompus** : ce chemin ne concerne que `failed`.
  `interrupted` a déjà sa `TerminationCause`, et elle est affichée.
