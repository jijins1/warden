"""Vérifie le ROUTAGE que la chart produit, pour chaque architecture d'ingress.

Ce script existe pour des pannes qui ne lèvent AUCUNE erreur :

  - un `-}}` de trop avant un `---` colle le séparateur à la ligne précédente,
    les deux documents FUSIONNENT, et la ressource du premier disparaît. C'est
    arrivé à l'Ingress du rôle `git` dès qu'un hôte d'interface était posé —
    donc sur toute installation servant une UI, et sans Ingress git aucun
    `warden push` n'aboutit ;

  - le préfixe de montage peut cesser d'atteindre le serveur, qui répondrait
    alors 404 sur toutes ses routes ;

  - la RACINE de l'API peut disparaître au profit du seul préfixe, ce qui
    casserait tous les clients CLI d'un coup — la moitié des appelants, sans que
    l'interface, elle, s'en aperçoive.

`helm template` et `helm lint` réussissent dans les deux cas, et le manifeste
produit reste du YAML valide. Seule la ressource, ou la variable, manque.
"""

import subprocess
import sys

import yaml

API = "warden-dev.ruokki.ovh"
GIT = "warden-git-dev.ruokki.ovh"
UI = "warden-ui-dev.ruokki.ovh"

# (titre, arguments helm, routage attendu, préfixe attendu)
CAS = [
    (
        "défauts — l'API à la racine, pas d'interface",
        [],
        [f"{API} / warden-api", f"{GIT} / warden-git-push"],
        "",
    ),
    (
        "deux origines — l'interface appelle l'API sur l'autre hôte",
        ["--set", "ingress.ui.host=" + UI, "--set", "ingress.ui.apiPath="],
        [f"{API} / warden-api", f"{GIT} / warden-git-push", f"{UI} / warden-ui"],
        "",
    ),
    (
        "même origine — l'hôte de l'interface route aussi /api",
        ["--set", "ingress.ui.host=" + UI],
        [
            f"{API} / warden-api",
            f"{GIT} / warden-git-push",
            f"{UI} / warden-ui",
            f"{UI} /api warden-api",
        ],
        "/api",
    ),
]


def rendre(args):
    sortie = subprocess.run(
        ["helm", "template", "warden", "charts/warden", "-n", "warden", *args],
        capture_output=True, text=True, check=True,
    ).stdout
    return [d for d in yaml.safe_load_all(sortie) if d]


def routage(docs):
    lignes = []
    for d in docs:
        if d["kind"] != "Ingress":
            continue
        for regle in d["spec"]["rules"]:
            for chemin in regle["http"]["paths"]:
                service = chemin["backend"]["service"]["name"]
                lignes.append(f"{regle['host']} {chemin['path']} {service}")
    return sorted(lignes)


def prefixe(docs):
    for d in docs:
        if d["kind"] == "Deployment" and d["metadata"]["name"].endswith("-api"):
            env = d["spec"]["template"]["spec"]["containers"][0]["env"]
            for e in env:
                if e["name"] == "WARDEN_API_BASE_PATH":
                    return e["value"]
    return ""


def main():
    echec = False
    for titre, args, routes_attendues, prefixe_attendu in CAS:
        docs = rendre(args)

        obtenu = routage(docs)
        if obtenu != sorted(routes_attendues):
            print(f"::error::{titre} — routage inattendu")
            print(f"  obtenu  : {obtenu}")
            print(f"  attendu : {sorted(routes_attendues)}")
            echec = True
            continue

        vu = prefixe(docs)
        if vu != prefixe_attendu:
            print(f"::error::{titre} — WARDEN_API_BASE_PATH « {vu} », attendu « {prefixe_attendu} »")
            echec = True
            continue

        print(f"ok — {titre}")

    return 1 if echec else 0


if __name__ == "__main__":
    sys.exit(main())
