# La cible de test du CLUSTER, distincte de celle des tests d'enrôlement.
#
# Celle des tests (`internal/runner/internal/enroll/testdata/cible`) est
# délibérément NUE — sans Python — parce qu'elle sert à prouver qu'un enrôlement
# fonctionne sur une machine fraîchement installée, ce qui est la raison pour
# laquelle warden n'utilise pas `ansible.posix.authorized_key`.
#
# Celle-ci sert à autre chose : faire tourner un vrai playbook. Ansible exécute
# ses modules EN PYTHON sur la machine distante, donc une machine gérée en a un.
# Garder les deux séparées évite d'affaiblir la première pour les besoins de la
# seconde.
FROM gitea.ruokki.ovh/ruokki/warden-cible-enrolement:dev

USER root
RUN apk add --no-cache python3
USER root
