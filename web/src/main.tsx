// Styles first, so tokens and base rules load before any component CSS.
import '@/shared/styles/index.css'
import '@/shared/theme/theme'

import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { z } from 'zod'
import { queryClient } from '@/app/query-client'
import { router } from '@/app/router'

// The Content-Security-Policy forbids eval, so tell Zod not to probe for it.
z.config({ jitless: true })

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)
