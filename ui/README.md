# Warden UI - Audit Interface

> Interface utilisateur pour la consultation de l'audit de **warden** - le proxy d'exécution Ansible.
> **Version : Angular 21 avec Signaux** 🚀

## 📋 Aperçu

Cette UI en **Angular 21** + **Material** utilise les **Signaux** (signals) pour une gestion d'état réactive et moderne.

Fonctionnalités :

- **Consulter les exécutions** : Liste paginée avec filtres (environnement, inventaire, état, date, etc.)
- **Voir les détails** : Événements en temps réel via WebSocket, diffs git, résumé
- **Explorer par machine** : Vue des exécutions ayant touché une machine spécifique
- **Analyser par environnement** : Statistiques et historique par environnement
- **Tableau de bord** : Vue d'ensemble avec métriques clés

## 🚀 Démarrage rapide

### Prérequis

- Node.js ≥ 20
- npm ≥ 9
- Angular CLI ≥ 21

### Installation

```bash
# Installer les dépendances
npm install

# Démarrer en mode développement
npm start
```

L'application sera disponible sur [http://localhost:3000](http://localhost:3000).

### Configuration

Créer un fichier `.env` pour configurer l'URL de l'API :

```bash
NG_APP_API_URL=http://localhost:8080
NG_APP_WS_URL=ws://localhost:8080
NG_APP_INSECURE_DEV_AUTH=true
```

## 📦 Build

### Build pour le développement

```bash
npm run build:dev
```

### Build pour la production

```bash
npm run build
```

Les fichiers sont générés dans `dist/warden-ui`.

## 🎯 Architecture avec Signaux

Cette UI utilise **Angular Signals** (v21) pour :

### ✨ Avantages des Signaux
- **Réactivité fine** : Mise à jour automatique de l'UI sans `ChangeDetectionStrategy.Default`
- **Pas de Subscription hell** : Plus besoin de `subscribe()` + `unsubscribe()` manuel
- **Code plus simple** : État centralisé dans des services avec `signal()` et `computed()`
- **Performances optimisées** : Angular optimise les mises à jour

### Exemples dans le code

```typescript
// Service avec signaux
@Injectable({ providedIn: 'root' })
export class RunsService {
  private readonly _runs = signal<RunListItem[]>([]);
  private readonly _loading = signal<boolean>(false);
  
  readonly runs = this._runs.asReadonly();
  readonly loading = this._loading.asReadonly();
  
  listRuns(filter: RunFilter): void {
    this._loading.set(true);
    this.http.get<RunListResult>(url).subscribe({
      next: (result) => {
        this._runs.set(result.runs);
        this._loading.set(false);
      }
    });
  }
}

// Composant avec effects
@Component({...})
export class RunsPageComponent {
  constructor(public runsService: RunsService) {
    effect(() => {
      // Mise à jour automatique quand runsService.runs() change
      this.dataSource.data = this.runsService.runs();
    });
  }
}
```

## 🏗️ Architecture

```
ui/
├── src/
│   ├── app/
│   │   ├── models/          # Interfaces TypeScript (Run, Host, Environment, etc.)
│   │   ├── services/        # Services API avec signaux (RunsService, EventsService, etc.)
│   │   ├── pages/           # Pages principales (Dashboard, Runs, RunDetail, etc.)
│   │   ├── guards/          # Gardes de route
│   │   ├── interceptors/    # Intercepteurs HTTP
│   │   ├── app.component.ts # Composant racine
│   │   ├── app.routes.ts   # Configuration du routage
│   │   └── app.config.ts   # Configuration Angular
│   ├── styles.scss         # Styles globaux + thème Material
│   ├── index.html          # Page HTML principale
│   └── main.ts             # Point d'entrée
├── package.json
├── angular.json
├── Dockerfile
├── nginx.conf
└── Makefile
```

## 🎯 Fonctionnalités

### Pages disponibles

| Route | Description |
|-------|-------------|
| `/` | Tableau de bord avec statistiques |
| `/runs` | Liste des exécutions avec filtres |
| `/runs/{runRef}` | Détails d'une exécution (events, diff, résumé) |
| `/hosts` | Liste des machines enrôlées |
| `/hosts/{host}` | Exécutions pour une machine spécifique |
| `/environments` | Liste des environnements |

### Filtres supportés

- **Par environnement** : `?env=production`
- **Par inventaire** : `?inventory=web-servers`
- **Par état** : `?state=failed` (pending, running, succeeded, failed, cancelled)
- **Par date** : `?since=2024-01-01T00:00:00Z&until=2024-01-31T23:59:59Z`
- **Changements minimum** : `?changed_min=5`
- **Pagination** : `?limit=50&cursor=...`

### WebSocket

Les événements en temps réel sont streamés via WebSocket depuis `/runs/{runRef}/stream`.

## 🎨 Thème Material

Le thème est personnalisé avec :

- **Couleur primaire** : Indigo (#1565c0)
- **Couleur d'accent** : Amber (#ffa000)
- **Couleur d'avertissement** : Rouge (#c62828)

## 🔒 Authentification

En mode développement (`NG_APP_INSECURE_DEV_AUTH=true`), l'authentification utilise le header `X-User-Subject` avec une valeur stockée dans `localStorage`.

Pour la production, il faut implémenter un vrai fournisseur d'authentification (OIDC, mTLS, etc.) et mettre à jour l'intercepteur dans `src/app/interceptors/auth.interceptor.ts`.

## 📊 API Backend

L'UI consomme les endpoints suivants :

| Endpoint | Méthode | Description |
|----------|---------|-------------|
| `/api/runs` | GET | Liste des exécutions |
| `/api/runs/{runRef}` | GET | Détails d'une exécution |
| `/api/runs/{runRef}/stream` | GET (WS) | Stream des événements |
| `/api/runs/{runRef}/diff` | GET | Diff git de l'exécution |
| `/api/hosts/{host}/runs` | GET | Exécutions par machine |
| `/api/admin/hosts` | GET | Liste des machines |
| `/api/admin/environments` | GET | Liste des environnements |

## 🐳 Docker

### Build de l'image

```bash
make docker
# ou
make docker DOCKER_TAG=v1.0.0
```

### Push vers le registry

```bash
make docker:push
```

### Exécuter avec Docker

```bash
docker run -p 80:80 -e NG_APP_API_URL=http://host.docker.internal:8080 ghcr.io/jijins1/warden-ui:latest
```

## 📄 Licence

Ce projet fait partie de **warden** et suit la même licence.

## 🤝 Contribuer

Voir le [README principal](../../README.md) pour les détails sur le projet.

## 📚 Ressources Angular 21

- [Angular Signals Documentation](https://angular.dev/guide/signals)
- [Angular 21 Release Notes](https://blog.angular.io/angular-v21-is-now-available-1b002c1b204d)
- [Migration Guide](https://angular.dev/guide/update-to-version-21)
