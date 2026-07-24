import { useEffect, useState } from "react";
import { Loader2, Palette, Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { SectionHeader, SettingRow } from "./shared";
import { api, isApiError, type Branding } from "@/lib/api";

export default function AdminBranding() {
  const { t } = useTranslation();
  const [data, setData] = useState<Branding | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.get<Branding>("/api/admin/branding").then(setData).catch(() => {});
  }, []);

  async function save(patch: Partial<Branding>) {
    setSaving(true);
    setSaved(false);
    setError(null);
    try {
      const res = await api.putRaw<Branding>("/api/admin/branding", patch);
      setData(res);
      setSaved(true);
      setTimeout(() => setSaved(false), 3000);
    } catch (err) {
      setError(isApiError(err) ? err.error_description ?? err.error : "error");
    } finally {
      setSaving(false);
    }
  }

  if (!data) return (
    <>
      <SectionHeader icon={Palette} titleKey="admin.branding.title" descKey="admin.branding.description" />
      <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
    </>
  );

  return (
    <>
      <SectionHeader icon={Palette} titleKey="admin.branding.title" descKey="admin.branding.description" />
      {error && <p className="text-sm text-destructive">{error}</p>}
      {saved && <p className="text-sm text-emerald-600 dark:text-emerald-400">{t("admin.branding.saved")}</p>}

      <Card>
        <CardContent className="divide-y divide-border p-0">
          <SettingRow title={t("admin.branding.workspaceName")} description={t("admin.branding.workspaceNameDesc")}>
            <div className="flex items-center gap-2">
              <Input
                defaultValue={data.workspace_name}
                className="h-8 w-48 text-[13px]"
                onChange={(e) => setData({ ...data, workspace_name: e.target.value })}
              />
              <Button size="sm" variant="outline" disabled={saving} onClick={() => save({ workspace_name: data.workspace_name })}>
                <Save className="h-3 w-3" />
              </Button>
            </div>
          </SettingRow>

          <SettingRow title={t("admin.branding.primaryColor")} description={t("admin.branding.primaryColorDesc")}>
            <div className="flex items-center gap-2">
              <input
                type="color"
                className="h-8 w-8 cursor-pointer rounded border border-border"
                value={hslToHex(data.primary_color)}
                onChange={(e) => {
                  const hsl = hexToHsl(e.target.value);
                  setData({ ...data, primary_color: hsl });
                }}
              />
              <Button size="sm" variant="outline" disabled={saving} onClick={() => save({ primary_color: data.primary_color })}>
                <Save className="h-3 w-3" />
              </Button>
            </div>
          </SettingRow>

          <SettingRow title={t("admin.branding.logo")} description={t("admin.branding.logoDesc")}>
            {data.logo_url ? (
              <img src={data.logo_url} alt="logo" className="h-8 w-8 rounded border border-border object-cover" />
            ) : (
              <Button variant="outline" size="sm" disabled>{t("admin.branding.upload")}</Button>
            )}
          </SettingRow>
        </CardContent>
      </Card>
    </>
  );
}

// Convert "215 55% 48%" (CSS HSL without hsl() wrapper) to #hex for the color input.
function hslToHex(hsl: string): string {
  const parts = hsl.split(/\s+/);
  if (parts.length < 3) return "#3777c4";
  const h = parseFloat(parts[0]);
  const s = parseFloat(parts[1]) / 100;
  const l = parseFloat(parts[2]) / 100;
  const a = s * Math.min(l, 1 - l);
  const f = (n: number) => {
    const k = (n + h / 30) % 12;
    const c = l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1));
    return Math.round(255 * c).toString(16).padStart(2, "0");
  };
  return `#${f(0)}${f(8)}${f(4)}`;
}

// Convert #hex to "H S% L%" format for CSS variables.
function hexToHsl(hex: string): string {
  const r = parseInt(hex.slice(1, 3), 16) / 255;
  const g = parseInt(hex.slice(3, 5), 16) / 255;
  const b = parseInt(hex.slice(5, 7), 16) / 255;
  const max = Math.max(r, g, b), min = Math.min(r, g, b);
  const l = (max + min) / 2;
  let h = 0, s = 0;
  if (max !== min) {
    const d = max - min;
    s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
    switch (max) {
      case r: h = ((g - b) / d + (g < b ? 6 : 0)); break;
      case g: h = ((b - r) / d + 2); break;
      case b: h = ((r - g) / d + 4); break;
    }
    h *= 60;
  }
  return `${Math.round(h)} ${Math.round(s * 100)}% ${Math.round(l * 100)}%`;
}
