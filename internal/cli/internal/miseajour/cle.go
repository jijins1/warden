package miseajour

// ClePublique est la clé minisign qui signe les releases de la CLI : la
// deuxième ligne du `warden.pub` produit par `minisign -G` (ADR-0049).
//
// Inscrite EN DUR, et c'est tout le point : une clé venue du serveur ou de
// l'hôte de téléchargement ne protégerait de rien. Vide, la mise à jour est
// refusée — une CLI sans clé ne télécharge rien.
//
// La changer demande une release intermédiaire, qui embarque la nouvelle clé
// et reste signée par l'ancienne.
const ClePublique = "RWQ7WndJJb1jcrTKvaqCztgrcEHMgOFq91MFou/hR01C7raUj6OjXxm0"
