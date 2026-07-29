import { Outlet } from "react-router-dom";

import { PageWrapper } from "@/components/page";

// AdminLayout wraps all admin sub-routes with the same content width and
// spacing as the rest of the authenticated app.
export default function AdminLayout() {
  return (
    <PageWrapper>
      <Outlet />
    </PageWrapper>
  );
}
