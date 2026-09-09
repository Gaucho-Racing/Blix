import "@fontsource-variable/geist"
import "@fontsource-variable/geist-mono"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import { Navigate, RouterProvider, createBrowserRouter } from "react-router-dom"

import { AppShell } from "@/components/app-shell"
import { RunDetailsPage } from "@/pages/run-details-page"
import { RunsPage } from "@/pages/runs-page"
import "@/index.css"

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 2,
      refetchOnWindowFocus: true,
    },
  },
})

const router = createBrowserRouter([
  {
    path: "/",
    element: <AppShell><RunsPage /></AppShell>,
  },
  {
    path: "/runs",
    element: <AppShell><RunsPage /></AppShell>,
  },
  {
    path: "/runs/:runID",
    element: <AppShell><RunDetailsPage /></AppShell>,
  },
  {
    path: "*",
    element: <Navigate replace to="/runs" />,
  },
])

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)
