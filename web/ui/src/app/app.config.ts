import {
  ApplicationConfig,
  inject,
  provideAppInitializer,
  provideBrowserGlobalErrorListeners,
} from '@angular/core';
import { provideRouter, withComponentInputBinding } from '@angular/router';
import { provideHttpClient, withInterceptors } from '@angular/common/http';
import { MatIconRegistry } from '@angular/material/icon';
import { routes } from './app.routes';
import { credentialsInterceptor, errorInterceptor } from './core/http/api.interceptor';

export const appConfig: ApplicationConfig = {
  providers: [
    provideBrowserGlobalErrorListeners(),
    provideRouter(routes, withComponentInputBinding()),
    // L'ordre COMPTE : `credentials` doit voir la requête sortante avant que
    // `error` n'observe la réponse. Angular applique la chaîne dans l'ordre à
    // l'aller et en sens inverse au retour, donc celui-ci met bien la
    // normalisation d'erreur au plus près de l'appelant.
    provideHttpClient(withInterceptors([credentialsInterceptor, errorInterceptor])),
    // Le jeu `outlined` plutôt que le plein : sur une table dense, des icônes
    // pleines pèsent autant que la donnée qu'elles accompagnent.
    provideAppInitializer(() => {
      inject(MatIconRegistry).setDefaultFontSetClass('material-icons-outlined');
    }),
  ],
};
