import "@fontsource-variable/geist"
import "@fontsource-variable/geist-mono"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { Bot, BookOpen, CalendarClock, MessageSquare, Wrench } from "lucide-react"
import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import { Navigate, RouterProvider, createBrowserRouter } from "react-router-dom"

import { AppShell } from "@/components/app-shell"
import { AdminOverviewPage } from "@/pages/admin-overview-page"
import { LegacyRunRedirect } from "@/pages/legacy-run-redirect"
import { PlaceholderPage } from "@/pages/placeholder-page"
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
    element: <AppShell />,
    children: [
      { index: true, element: <Navigate replace to="/chat" /> },
      {
        path: "chat",
        element: (
          <PlaceholderPage
            description="Ask questions, pull in team context, and hand work off to Blix."
            eyebrow="WORKSPACE"
            icon={MessageSquare}
            label="Conversation"
            title="Chat with Blix"
          />
        ),
      },
      {
        path: "knowledge",
        element: (
          <PlaceholderPage
            description="Search team documentation, Discord history, and indexed internal knowledge."
            eyebrow="WORKSPACE"
            icon={BookOpen}
            label="Knowledge base"
            title="Team knowledge"
          />
        ),
      },
      {
        path: "automations",
        element: (
          <PlaceholderPage
            description="Create, schedule, and monitor recurring work and agent runbooks."
            eyebrow="WORKSPACE"
            icon={CalendarClock}
            label="Autonomous work"
            title="Automations"
          />
        ),
      },
      { path: "admin", element: <AdminOverviewPage /> },
      { path: "admin/runs", element: <RunsPage /> },
      { path: "admin/runs/:runID", element: <RunDetailsPage /> },
      {
        path: "admin/agents",
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
        path: "admin/tools",
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
      { path: "runs", element: <LegacyRunRedirect /> },
      { path: "runs/:runID", element: <LegacyRunRedirect /> },
      { path: "*", element: <Navigate replace to="/chat" /> },
    ],
  },
])

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)
