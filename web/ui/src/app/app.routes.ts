import { Routes } from '@angular/router';
import { authGuard, adminGuard } from './core/guards/auth/auth.guard';

export const routes: Routes = [
  {
    path: 'login',
    title: 'Sign in — warden',
    loadComponent: () => import('./features/auth/login/login').then((m) => m.Login),
  },
  {
    // La coquille porte la garde, donc TOUT ce qui vit dessous en hérite. Une
    // garde par route laisserait la prochaine s'ajouter sans, et l'oubli ne se
    // verrait pas.
    path: '',
    canActivate: [authGuard],
    loadComponent: () => import('./layout/shell/shell').then((m) => m.Shell),
    children: [
      {
        path: 'runs',
        title: 'Runs — warden',
        loadComponent: () => import('./features/runs/liste-runs/liste-runs').then((m) => m.ListeRuns),
      },
      {
        // AVANT `runs/:runRef` : un segment statique doit être essayé avant le
        // paramétré, sinon `/runs/new` s'y ferait lire comme `runRef == 'new'`.
        //
        // `authGuard` seul, hérité de la coquille — PAS `adminGuard`. Lancer un
        // run demande un droit d'accès sur l'ENVIRONNEMENT (ADR-0042), la même
        // autorisation que `warden submit` ; ce n'est pas une action
        // d'administrateur, contrairement à `repos` et `environments`
        // ci-dessous, qui exposent une vue de l'organisation entière.
        path: 'runs/new',
        title: 'Launch a run — warden',
        loadComponent: () => import('./features/runs/lancement-run/lancement-run').then((m) => m.LancementRun),
      },
      {
        // `runRef` est lié par `withComponentInputBinding` : c'est ce qui rend
        // un lien profond rechargeable, et la raison d'être de
        // `GET /runs/{run_ref}`.
        path: 'runs/:runRef',
        title: 'Run — warden',
        loadComponent: () => import('./features/runs/run-detail/run-detail').then((m) => m.RunDetail),
      },
      {
        // `authGuard` seul : `GET /hosts` se restreint lui-même aux
        // environnements où l'appelant a un droit vivant, administrateur ou non.
        path: 'hosts',
        title: 'Machines — warden',
        loadComponent: () =>
          import('./features/hosts/liste-machines/liste-machines').then((m) => m.ListeMachines),
      },
      {
        path: 'hosts/:host',
        title: 'Machine — warden',
        loadComponent: () => import('./features/hosts/machine/machine').then((m) => m.Machine),
      },
      {
        // `authGuard` seul, comme `runs/new` : la clé de vault d'un
        // environnement se gère avec le droit d'y lancer un run (ADR-0045),
        // pas avec l'administration. Deux chemins, un composant.
        path: 'vault',
        title: 'Vault keys — warden',
        loadComponent: () => import('./features/vault/vault/vault').then((m) => m.Vault),
      },
      {
        path: 'vault/:environment',
        title: 'Vault keys — warden',
        loadComponent: () => import('./features/vault/vault/vault').then((m) => m.Vault),
      },
      {
        path: 'account',
        title: 'Account — warden',
        loadComponent: () => import('./features/account/account/account').then((m) => m.Account),
      },
      {
        // `adminGuard` en plus de la garde de la coquille : `GET /admin/repos`
        // rend une carte de l'organisation (voir HandleListRepos), pas une vue
        // restreinte à l'appelant comme le reste de l'UI.
        path: 'repos',
        title: 'Repos — warden',
        canActivate: [adminGuard],
        loadComponent: () => import('./features/repos/repos/repos').then((m) => m.Repos),
      },
      {
        // Même raisonnement que `repos` : `GET /admin/keys` est réservé aux
        // administrateurs côté serveur (ADR-0020).
        path: 'environments',
        title: 'Environments — warden',
        canActivate: [adminGuard],
        loadComponent: () =>
          import('./features/environments/environnements/environnements').then((m) => m.Environnements),
      },
      { path: '', pathMatch: 'full', redirectTo: 'runs' },
    ],
  },
  { path: '**', redirectTo: '' },
];
