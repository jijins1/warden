# Complétion zsh pour warden.
#
# Générée par « warden completion zsh ». Chaque Tab relance
# « warden __complete » avec les mots déjà tapés : c'est le même arbre de
# commandes que « warden help », donc jamais en désaccord avec lui.
#
# Le backend rend « mot<TAB>description » ; `_describe` attend « mot:description »
# et affiche « mot  -- description », une ligne par candidat.
__warden_complete() {
    local -a mots lignes candidats
    local ligne mot
    local IFS=$'\n'
    # `(@)` force le contexte tableau sur la coupe : sans lui, zsh rejoint la
    # coupe en une chaîne puis la re-sépare, et perd le dernier mot quand il
    # est vide — exactement le cas d'un Tab juste après un espace.
    mots=("${(@)words[2,CURRENT]}")
    lignes=($(warden __complete "${mots[@]}" 2>/dev/null))
    if (( ! ${#lignes[@]} )); then
        _files
        return
    fi
    for ligne in "${lignes[@]}"; do
        # Un `:` dans le mot séparerait la description trop tôt.
        mot=${ligne%%$'\t'*}
        mot=${mot//:/\\:}
        if [[ $ligne == *$'\t'* ]]; then
            candidats+=("${mot}:${ligne#*$'\t'}")
        else
            candidats+=("$mot")
        fi
    done
    _describe -t candidats 'warden' candidats
}
compdef __warden_complete warden
