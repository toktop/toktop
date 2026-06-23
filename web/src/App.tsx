import { Routes, Route } from "react-router-dom"

import { Layout }          from "@/components/layout"
import { DashboardPage }   from "@/pages/dashboard"
import { SessionsPage }    from "@/pages/sessions"
import { SessionDetailPage } from "@/pages/session-detail"
import { SearchPage }      from "@/pages/search"
import { AnalyticsPage }   from "@/pages/analytics"
import { DaemonPage }      from "@/pages/daemon"
import { SettingsPage }    from "@/pages/settings"

export function App() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index             element={<DashboardPage />} />
        <Route path="sessions"   element={<SessionsPage />} />
        <Route path="sessions/:id" element={<SessionDetailPage />} />
        <Route path="search"     element={<SearchPage />} />
        <Route path="analytics"  element={<AnalyticsPage />} />
        <Route path="daemon"     element={<DaemonPage />} />
        <Route path="settings"   element={<SettingsPage />} />
      </Route>
    </Routes>
  )
}

export default App
