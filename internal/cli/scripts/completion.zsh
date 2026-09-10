# Complétion zsh pour warden.
#
# Générée par « warden completion zsh ». Chaque Tab relance
# « warden __complete » avec les mots déjà tapés : c'est le même arbre de
# commandes que « warden help », donc jamais en désaccord avec lui.
__warden_complete() {
    local -a mots candidats
    local IFS=$'\n'
    # `(@)` force le contexte tableau sur la coupe : sans lui, zsh rejoint la
    # coupe en une chaîne puis la re-sépare, et perd le dernier mot quand il
    # est vide — exactement le cas d'un Tab juste après un espace.
    mots=("${(@)words[2,CURRENT]}")
    candidats=($(warden __complete "${mots[@]}" 2>/dev/null))
    if (( ${#candidats[@]} )); then
        compadd -a candidats
    else
        _files
    fi
}
compdef __warden_complete warden
