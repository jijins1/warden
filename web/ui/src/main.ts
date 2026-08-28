import { bootstrapApplication } from '@angular/platform-browser';
import { appConfig } from './app/app.config';
import { APP_CONFIG, chargerConfig } from './app/core/config/app-config';
import { App } from './app/app';

// La configuration est chargée AVANT le bootstrap, et non par un initialiseur.
//
// C'est ce qui garantit qu'aucun injecteur ne peut exister sans elle : un
// service construit pendant le démarrage ne peut pas tomber sur une adresse
// d'API encore vide. Et l'échec est franc — pas d'application du tout plutôt
// qu'une application qui interroge un serveur deviné.
chargerConfig()
  .then((config) =>
    bootstrapApplication(App, {
      ...appConfig,
      providers: [...appConfig.providers, { provide: APP_CONFIG, useValue: config }],
    }),
  )
  .catch((err: unknown) => {
    console.error('warden: démarrage impossible', err);
    document.body.textContent = 'warden: configuration illisible (config.json)';
  });
