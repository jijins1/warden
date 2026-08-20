import { Injectable } from '@angular/core';
import { HttpInterceptor, HttpRequest, HttpHandler, HttpEvent } from '@angular/common/http';
import { Observable } from 'rxjs';

import { ConfigService } from '../services/config.service';

@Injectable()
export class AuthInterceptor implements HttpInterceptor {
  constructor(private config: ConfigService) {}

  intercept(request: HttpRequest<any>, next: HttpHandler): Observable<HttpEvent<any>> {
    // In dev mode with insecure auth, add the user subject from localStorage
    if (this.config.isInsecureDevAuth()) {
      const userSubject = this.config.getUserSubject();
      if (userSubject) {
        request = request.clone({
          setHeaders: {
            'X-User-Subject': userSubject
          }
        });
      }
    }

    // For production, you would add the actual auth token here
    // const token = this.authService.getToken();
    // if (token) {
    //   request = request.clone({
    //     setHeaders: {
    //       Authorization: `Bearer ${token}`
    //     }
    //   });
    // }

    return next.handle(request);
  }
}
