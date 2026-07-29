import { type ReactNode } from "react";

// PageWrapper provides consistent padding, max-width, and spacing for every
// authenticated page. Keeps the content area readable without per-page
// duplication.
export function PageWrapper({
  children,
  className = "",
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={`p-6 lg:p-10 ${className}`}>
      <div className="mx-auto w-full max-w-5xl space-y-8">{children}</div>
    </div>
  );
}

// PageHeader is the title block at the top of each page.
export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {description && (
          <p className="text-sm text-muted-foreground">{description}</p>
        )}
      </div>
      {actions && <div className="shrink-0">{actions}</div>}
    </div>
  );
}
