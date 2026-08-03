// Password health report dialog.
//
// Runs the client-side vault analyser (weak / reused / old passwords) over the
// decrypted item cache and summarises the result in a modal. Each entry links
// to the item so the user can rotate the password immediately.

import { useMemo, useState } from "react";
import { AlertTriangle, HeartPulse, KeyRound, RotateCw } from "lucide-react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { analyzeVaultHealth } from "@/lib/vault/health";
import { useVault } from "@/lib/vault/store";

export function HealthButton() {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
        <HeartPulse className="h-4 w-4" />
        {t("passwords.health.button")}
      </Button>
      <HealthDialog open={open} onClose={() => setOpen(false)} />
    </>
  );
}

function HealthDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const { items } = useVault();
  const report = useMemo(() => analyzeVaultHealth(items), [items]);

  const allGood =
    report.weak.length === 0 &&
    report.reused.length === 0 &&
    report.old.length === 0;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t("passwords.health.title")}
      description={t("passwords.health.description", {
        total: report.totalPasswords,
      })}
    >
      <div className="space-y-4">
        {allGood ? (
          <p className="rounded-md border border-green-500/40 bg-green-500/10 px-3 py-4 text-center text-sm text-green-700 dark:text-green-400">
            {t("passwords.health.allGood")}
          </p>
        ) : (
          <>
            {report.weak.length > 0 && (
              <HealthSection
                icon={<AlertTriangle className="h-4 w-4 text-amber-500" />}
                title={t("passwords.health.weak", {
                  count: report.weak.length,
                })}
              >
                {report.weak.map((e) => (
                  <ItemLink
                    key={e.item.id}
                    id={e.item.id}
                    name={e.item.name}
                    meta={e.reasons
                      .map((r) => t(`passwords.health.reasons.${r}`))
                      .join(", ")}
                  />
                ))}
              </HealthSection>
            )}

            {report.reused.length > 0 && (
              <HealthSection
                icon={<KeyRound className="h-4 w-4 text-red-500" />}
                title={t("passwords.health.reused", {
                  count: report.reused.reduce((n, g) => n + g.items.length, 0),
                })}
              >
                {report.reused.map((g, i) => (
                  <div key={i} className="space-y-0.5">
                    {g.items.map((it) => (
                      <ItemLink
                        key={it.id}
                        id={it.id}
                        name={it.name}
                        meta={t("passwords.health.reusedCount", {
                          count: g.items.length,
                        })}
                      />
                    ))}
                  </div>
                ))}
              </HealthSection>
            )}

            {report.old.length > 0 && (
              <HealthSection
                icon={<RotateCw className="h-4 w-4 text-blue-500" />}
                title={t("passwords.health.old", { count: report.old.length })}
              >
                {report.old.slice(0, 20).map((e) => (
                  <ItemLink
                    key={e.item.id}
                    id={e.item.id}
                    name={e.item.name}
                    meta={t("passwords.health.ageDays", {
                      count: e.ageDays,
                    })}
                  />
                ))}
              </HealthSection>
            )}
          </>
        )}
      </div>
    </Modal>
  );
}

function HealthSection({
  icon,
  title,
  children,
}: {
  icon: React.ReactNode;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1">
      <p className="flex items-center gap-1.5 text-sm font-medium">
        {icon}
        {title}
      </p>
      <div className="space-y-0.5">{children}</div>
    </div>
  );
}

function ItemLink({
  id,
  name,
  meta,
}: {
  id: string;
  name: string;
  meta: string;
}) {
  return (
    <Link
      to={`/passwords/${id}`}
      className="flex items-center justify-between gap-2 rounded-md border bg-muted/20 px-3 py-1.5 text-sm hover:bg-accent/50"
    >
      <span className="truncate">{name}</span>
      <span className="shrink-0 text-xs text-muted-foreground">{meta}</span>
    </Link>
  );
}
