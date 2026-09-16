import "@fontsource-variable/geist"
import "@fontsource-variable/geist-mono"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { Bot, Wrench } from "lucide-react"
import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import { Navigate, RouterProvider, createBrowserRouter } from "react-router-dom"

import { AppShell } from "@/components/app-shell"
import { RequireAdmin } from "@/components/require-admin"
import { AdminOverviewPage } from "@/pages/admin-overview-page"
import { AutomationsPage } from "@/pages/automations-page"
import { ChatPage } from "@/pages/chat-page"
import { KnowledgePage } from "@/pages/knowledge-page"
import { KnowledgeReviewsPage, KnowledgeReviewDetailsPage } from "@/pages/knowledge-reviews-page"
import { LegacyRunRedirect } from "@/pages/legacy-run-redirect"
import { LoginPage } from "@/pages/login-page"
import { PlaceholderPage } from "@/pages/placeholder-page"
import { RunDetailsPage } from "@/pages/run-details-page"
import { RunsPage } from "@/pages/runs-page"
import { WorkersPage } from "@/pages/workers-page"
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
    element: <AppShell />,
    children: [
      { index: true, element: <Navigate replace to="/chat" /> },
      { path: "chat/:runID?", element: <ChatPage /> },
      { path: "knowledge", element: <KnowledgePage /> },
      { path: "automations", element: <AutomationsPage /> },
      {
        path: "admin",
        element: <RequireAdmin />,
        children: [
          { index: true, element: <AdminOverviewPage /> },
          { path: "runs", element: <RunsPage /> },
          { path: "runs/:runID", element: <RunDetailsPage /> },
          { path: "workers", element: <WorkersPage /> },
          { path: "knowledge", element: <KnowledgeReviewsPage /> },
          { path: "knowledge/changesets/:changesetID", element: <KnowledgeReviewDetailsPage /> },
          {
            path: "agents",
            element: (
              <PlaceholderPage
                description="Inspect agent definitions, responsibilities, and runtime availability."
                eyebrow="ADMIN"
                icon={Bot}
                label="Agent registry"
                title="Agents"
              />
            ),
          },
          {
            path: "tools",
            element: (
              <PlaceholderPage
                description="Review registered tools, supported versions, and configuration health."
                eyebrow="ADMIN"
                icon={Wrench}
                label="Tool registry"
                title="Tools"
              />
            ),
          },
        ],
      },
      { path: "runs", element: <LegacyRunRedirect /> },
      { path: "runs/:runID", element: <LegacyRunRedirect /> },
      { path: "*", element: <Navigate replace to="/chat" /> },
    ],
  },
  { path: "/auth/login", element: <LoginPage /> },
])

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)
