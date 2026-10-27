# Une machine gérée, pour la démonstration : sshd, Python (Ansible exécute ses
# modules en Python sur la cible), un compte `ansible` à mot de passe pour le
# premier contact de l'enrôlement assisté.
#
# Même construction que la cible des tests d'enrôlement
# (`internal/runner/internal/enroll/testdata/cible`), Python en plus : le mot
# de passe et la clé d'hôte sont posés au démarrage, jamais dans une couche.
FROM alpine:3.24

RUN apk add --no-cache openssh-server python3 \
 && adduser -D -u 4242 ansible

RUN sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication yes/' /etc/ssh/sshd_config \
 && sed -i 's/^#\?PubkeyAuthentication.*/PubkeyAuthentication yes/' /etc/ssh/sshd_config

CMD ["sh", "-c", "echo \"ansible:${CIBLE_PASSWORD:-hunter2}\" | chpasswd && ssh-keygen -A >/dev/null && exec /usr/sbin/sshd -D -e"]
