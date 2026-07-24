import { Outlet } from "react-router-dom";

// AdminLayout wraps all admin sub-routes. Padding matches the rest of the
// app (p-6 lg:p-10). space-y-8 on the inner wrapper ensures consistent
// vertical rhythm between SectionHeader and content blocks — section pages
// use <> fragments which don't create DOM nodes, so the children become
// direct siblings of this div and space-y applies correctly.
export default function AdminLayout() {
  return (
    <div className="p-6 lg:p-10">
      <div className="mx-auto max-w-4xl space-y-8">
        <Outlet />
      </div>
    </div>
  );
}
