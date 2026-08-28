import { ApplicationConfig, provideBrowserGlobalErrorListeners } from '@angular/core';
import { provideRouter, withComponentInputBinding } from '@angular/router';
import { provideHttpClient, withInterceptors } from '@angular/common/http';
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
  ],
};
