import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import { I18nProvider } from '../i18n'
import { Providers } from './providers'
import '../styles/index.css'

const root = document.getElementById('root')
if (!root) throw new Error('missing #root')
createRoot(root).render(
  <StrictMode>
    <Providers>
      <I18nProvider>
        <App />
      </I18nProvider>
    </Providers>
  </StrictMode>,
)
