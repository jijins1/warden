# Une machine à enrôler, jetable.
#
# Volontairement NUE : ni Python, ni sudo, ni rien d'autre qu'un sshd. C'est le
# cas qu'un enrôlement doit couvrir — une machine fraîchement installée — et la
# raison pour laquelle ce paquet n'utilise pas `ansible.posix.authorized_key`,
# qui exigerait Python sur la cible.
FROM alpine:3.22

RUN apk add --no-cache openssh-server \
 && adduser -D -u 4242 ansible \
 && echo 'ansible:hunter2' | chpasswd

# Un sshd de distribution moderne refuse le mot de passe par défaut. On
# l'autorise ICI, explicitement, parce que c'est le mode que l'enrôlement assisté
# utilise pour son PREMIER contact — et le rendre implicite masquerait le fait
# qu'une vraie machine peut parfaitement le refuser.
RUN sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication yes/' /etc/ssh/sshd_config \
 && sed -i 's/^#\?PubkeyAuthentication.*/PubkeyAuthentication yes/' /etc/ssh/sshd_config

# La clé d'hôte est frappée AU DÉMARRAGE, jamais figée dans l'image : une clé
# privée dans un dépôt git est une clé privée publiée. C'est aussi ce qui rend
# chaque conteneur distinct, donc l'épinglage réellement testable.
CMD ["sh", "-c", "ssh-keygen -A >/dev/null && exec /usr/sbin/sshd -D -e"]
