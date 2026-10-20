import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'
import { serverConfig } from './api/authConfig'
import { configureOIDC, userManager } from './api/oidc'

if (window.location.pathname === '/auth/silent') {
  // Inside the hidden SSO iframe: hand the provider's answer back to the
  // parent page and render nothing (no second app, no nested SSO attempt).
  void serverConfig().then(({ auth }) => {
    if (auth.oidc) configureOIDC(auth.oidc.issuer, auth.oidc.clientId, auth.oidc.metadata)
    void userManager()?.signinSilentCallback().catch(() => {})
  })
} else {
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  )
}
