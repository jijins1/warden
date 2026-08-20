import { Injectable } from '@angular/core';
import { CanActivate, ActivatedRouteSnapshot, RouterStateSnapshot, Router } from '@angular/router';

import { ConfigService } from '../services/config.service';

@Injectable({
  providedIn: 'root'
})
export class AuthGuard implements CanActivate {
  constructor(
    private config: ConfigService,
    private router: Router
  ) {}

  canActivate(
    route: ActivatedRouteSnapshot,
    state: RouterStateSnapshot
  ): boolean {
    // In dev mode, we allow access if a user subject is set
    if (this.config.isInsecureDevAuth()) {
      const userSubject = this.config.getUserSubject();
      if (userSubject) {
        return true;
      }
      // Redirect to login if no user
      this.router.navigate(['/login']);
      return false;
    }

    // In production, implement actual auth check
    // return this.authService.isAuthenticated();
    
    return true; // For now, allow access
  }
}
