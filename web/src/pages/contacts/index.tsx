import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type ChangeEvent,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import {
  Check,
  Copy,
  Download,
  Globe,
  Loader2,
  Mail,
  MapPin,
  MessageCircle,
  Pencil,
  Phone,
  Plus,
  Search,
  Star,
  Tags,
  Trash2,
  Upload,
  UserRound,
  Users,
  X,
} from "lucide-react";

import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Modal } from "@/components/ui/modal";
import { useConfirm } from "@/components/ui/confirm";
import { Textarea } from "@/components/ui/textarea";
import { PageHeader, PageWrapper } from "@/components/page";
import {
  contacts,
  type ContactGroup,
  type ContactPayload,
  type ContactRecord,
} from "@/lib/contacts";
import { cn } from "@/lib/utils";
import {
  contactToForm,
  displayName,
  emptyContact,
  errorMessage,
  formToPayload,
  hasIdentity,
  initials,
  newFormAddress,
  newFormValue,
  subtitle,
  type ContactFormState,
  type FormAddress,
  type FormValue,
} from "./helpers";

const PAGE_SIZE = 100;

export default function ContactsPage() {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const [searchParams, setSearchParams] = useSearchParams();
  const query = searchParams.get("q") ?? "";
  const activeGroup = searchParams.get("group") ?? "";
  const favoritesOnly = searchParams.get("favorite") === "1";

  function setParam(key: string, value: string | null) {
    const next = new URLSearchParams(searchParams);
    if (value) next.set(key, value);
    else next.delete(key);
    setSearchParams(next, { replace: true });
  }

  const [status, setStatus] = useState<{ enabled: boolean } | null>(null);
  const [items, setItems] = useState<ContactRecord[] | null>(null);
  const [groups, setGroups] = useState<ContactGroup[]>([]);
  const [total, setTotal] = useState(0);
  const [allTotal, setAllTotal] = useState(0);
  const [selectedID, setSelectedID] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [editing, setEditing] = useState<ContactRecord | null>(null);
  const [groupEditor, setGroupEditor] = useState<ContactGroup | "new" | null>(
    null,
  );
  const fileInput = useRef<HTMLInputElement>(null);

  const selected = useMemo(
    () =>
      items
        ? (items.find((item) => item.id === selectedID) ?? items[0] ?? null)
        : null,
    [items, selectedID],
  );
  const groupMap = useMemo(
    () => new Map(groups.map((group) => [group.id, group])),
    [groups],
  );

  const refreshGroups = useCallback(async () => {
    try {
      const res = await contacts.groups();
      setGroups(res.groups ?? []);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, []);

  const fetchPage = useCallback(
    async (offset: number, replace: boolean) => {
      if (offset === 0) setLoading(true);
      else setLoadingMore(true);
      setError(null);
      try {
        const list = await contacts.list({
          q: query,
          group: activeGroup,
          favorite: favoritesOnly,
          sort: "name",
          limit: PAGE_SIZE,
          offset,
        });
        setTotal(list.total ?? 0);
        // Track the unfiltered total so the "All contacts" badge is stable
        // while a search/favorite/group filter is active.
        if (!query && !activeGroup && !favoritesOnly) {
          setAllTotal(list.total ?? 0);
        }
        setItems((prev) =>
          replace
            ? (list.contacts ?? [])
            : [...(prev ?? []), ...(list.contacts ?? [])],
        );
        setSelectedID((cur) => {
          // On load-more keep the current selection; on a fresh filter fall
          // back to the first item of the new page. This avoids closing over
          // `items` so fetchPage's identity is stable across appends.
          if (!replace && cur) return cur;
          if (cur && list.contacts?.some((item) => item.id === cur)) return cur;
          return list.contacts?.[0]?.id ?? "";
        });
      } catch (e) {
        setError(errorMessage(e));
      } finally {
        setLoading(false);
        setLoadingMore(false);
      }
    },
    [activeGroup, favoritesOnly, query],
  );

  // Initial groups load — independent of search/group/filter.
  useEffect(() => {
    void refreshGroups();
  }, [refreshGroups]);

  // Reload list (debounced) whenever the filter params change.
  useEffect(() => {
    const handle = window.setTimeout(() => void fetchPage(0, true), 180);
    return () => window.clearTimeout(handle);
  }, [fetchPage]);

  useEffect(() => {
    contacts
      .status()
      .then((s) => setStatus({ enabled: s.enabled !== false }))
      .catch(() => setStatus({ enabled: true }));
  }, []);

  function flash(message: string | null) {
    setNotice(message);
    if (message) window.setTimeout(() => setNotice(null), 4000);
  }

  async function saveContact(payload: ContactPayload) {
    setBusy(true);
    setError(null);
    try {
      const saved = editing
        ? await contacts.update(editing.id, payload)
        : await contacts.create(payload);
      setEditing(null);
      await fetchPage(0, true);
      void refreshGroups();
      setSelectedID(saved.id);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  async function toggleFavorite(item: ContactRecord) {
    try {
      const next = await contacts.patch(item.id, {
        is_favorite: !item.is_favorite,
      });
      setItems((prev) =>
        (prev ?? []).map((row) => (row.id === next.id ? next : row)),
      );
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  async function removeContact(item: ContactRecord) {
    if (
      !(await confirm({
        title: t("contacts.confirmDelete", { name: displayName(item) }),
        destructive: true,
        confirmLabel: t("common.delete"),
        cancelLabel: t("common.cancel"),
      }))
    )
      return;
    setBusy(true);
    try {
      await contacts.remove(item.id);
      await fetchPage(0, true);
      void refreshGroups();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  async function uploadAvatar(item: ContactRecord, file?: File) {
    if (!file) return;
    setBusy(true);
    setError(null);
    try {
      const next = await contacts.uploadAvatar(item.id, file);
      setItems((prev) =>
        (prev ?? []).map((row) => (row.id === next.id ? next : row)),
      );
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  async function clearAvatar(item: ContactRecord) {
    setBusy(true);
    setError(null);
    try {
      const next = await contacts.clearAvatar(item.id);
      setItems((prev) =>
        (prev ?? []).map((row) => (row.id === next.id ? next : row)),
      );
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  async function importFile(file?: File) {
    if (!file) return;
    setBusy(true);
    setError(null);
    try {
      const res = await contacts.importVCF(file);
      await fetchPage(0, true);
      void refreshGroups();
      flash(
        res.failed
          ? t("contacts.importedWithFailures", {
              count: res.imported,
              failed: res.failed,
            })
          : t("contacts.imported", { count: res.imported }),
      );
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
      if (fileInput.current) fileInput.current.value = "";
    }
  }

  async function exportFile() {
    setBusy(true);
    try {
      const url = await contacts.exportVCF();
      const a = document.createElement("a");
      a.href = url;
      a.download = "contacts.vcf";
      document.body.appendChild(a);
      a.click();
      a.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), 10000);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  if (status && !status.enabled) {
    return (
      <PageWrapper>
        <PageHeader title={t("contacts.title")} />
        <Card className="p-6 text-sm text-muted-foreground">
          {t("contacts.disabled")}
        </Card>
      </PageWrapper>
    );
  }

  return (
    <PageWrapper className="h-full overflow-hidden">
      <PageHeader
        title={t("contacts.title")}
        description={t("contacts.description")}
        actions={
          <div className="flex items-center gap-2">
            <input
              ref={fileInput}
              type="file"
              accept=".vcf,text/vcard,text/x-vcard"
              className="hidden"
              onChange={(e) => void importFile(e.target.files?.[0])}
            />
            <Button
              variant="outline"
              size="sm"
              onClick={() => fileInput.current?.click()}
              disabled={busy}
              title={t("contacts.importBtn")}
            >
              <Upload className="h-4 w-4" />
              <span className="hidden sm:inline">
                {t("contacts.importBtn")}
              </span>
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => void exportFile()}
              disabled={busy}
              title={t("contacts.exportBtn")}
            >
              <Download className="h-4 w-4" />
              <span className="hidden sm:inline">
                {t("contacts.exportBtn")}
              </span>
            </Button>
            <Button size="sm" onClick={() => setEditing(emptyContact())}>
              <Plus className="h-4 w-4" />
              {t("contacts.addContact")}
            </Button>
          </div>
        }
      />

      {error && (
        <Banner tone="error" message={error} onClose={() => setError(null)} />
      )}
      {notice && (
        <Banner
          tone="success"
          message={notice}
          onClose={() => setNotice(null)}
        />
      )}

      <div className="grid min-h-[620px] gap-4 lg:grid-cols-[260px_minmax(320px,1fr)_340px]">
        <Card className="flex min-h-0 flex-col p-3">
          <div className="relative mb-3">
            <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setParam("q", e.target.value || null)}
              placeholder={t("contacts.searchPlaceholder")}
              className="pl-9"
            />
          </div>
          <div className="space-y-1">
            <FilterButton
              active={!activeGroup && !favoritesOnly}
              icon={<Users className="h-4 w-4" />}
              label={t("contacts.allContacts")}
              count={allTotal}
              onClick={() => {
                setParam("group", null);
                setParam("favorite", null);
              }}
            />
            <FilterButton
              active={favoritesOnly}
              icon={<Star className="h-4 w-4" />}
              label={t("contacts.favorites")}
              onClick={() => {
                setParam("group", null);
                setParam("favorite", "1");
              }}
            />
          </div>
          <div className="mt-4 flex items-center justify-between px-1 text-xs font-medium uppercase text-muted-foreground">
            <span>{t("contacts.groups")}</span>
            <button
              className="rounded p-1 hover:bg-accent"
              onClick={() => setGroupEditor("new")}
              aria-label={t("contacts.addGroup")}
            >
              <Plus className="h-3.5 w-3.5" />
            </button>
          </div>
          <div className="mt-1 min-h-0 flex-1 overflow-y-auto">
            {groups.map((group) => (
              <div key={group.id} className="group flex items-center gap-1">
                <button
                  className={cn(
                    "flex min-w-0 flex-1 items-center gap-2 rounded-md px-2 py-2 text-left text-sm hover:bg-accent",
                    activeGroup === group.id && "bg-accent",
                  )}
                  onClick={() => {
                    setParam("group", group.id);
                    setParam("favorite", null);
                  }}
                >
                  <span
                    className="h-2.5 w-2.5 rounded-full"
                    style={{ backgroundColor: group.color || "#64748b" }}
                  />
                  <span className="truncate">{group.name}</span>
                  <span className="ml-auto text-xs text-muted-foreground">
                    {group.count}
                  </span>
                </button>
                <button
                  className="rounded p-1 text-muted-foreground opacity-0 hover:bg-accent group-hover:opacity-100"
                  onClick={() => setGroupEditor(group)}
                  aria-label={t("contacts.editGroup")}
                >
                  <Pencil className="h-3.5 w-3.5" />
                </button>
              </div>
            ))}
          </div>
        </Card>

        <Card className="min-h-0 overflow-hidden p-0">
          {loading || items === null ? (
            <div className="flex h-full items-center justify-center">
              <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
            </div>
          ) : items.length === 0 ? (
            <EmptyContacts onCreate={() => setEditing(emptyContact())} />
          ) : (
            <div className="divide-y">
              {items.map((item) => (
                <button
                  key={item.id}
                  onClick={() => setSelectedID(item.id)}
                  aria-current={selected?.id === item.id ? "true" : undefined}
                  className={cn(
                    "flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-accent/60",
                    selected?.id === item.id && "bg-accent",
                  )}
                >
                  <ContactAvatar contact={item} />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <p className="truncate text-sm font-medium">
                        {displayName(item) || t("contacts.noName")}
                      </p>
                      {item.is_favorite && (
                        <Star className="h-3.5 w-3.5 fill-current text-amber-500" />
                      )}
                    </div>
                    <p className="truncate text-xs text-muted-foreground">
                      {subtitle(item)}
                    </p>
                  </div>
                  <div className="hidden gap-1 md:flex">
                    {item.group_ids.slice(0, 2).map((gid) => {
                      const group = groupMap.get(gid);
                      if (!group) return null;
                      return (
                        <Badge key={gid} variant="outline" className="max-w-24">
                          <span className="truncate">{group.name}</span>
                        </Badge>
                      );
                    })}
                  </div>
                </button>
              ))}
              {items.length < total && (
                <div className="p-3">
                  <Button
                    variant="ghost"
                    className="w-full"
                    onClick={() => void fetchPage(items.length, false)}
                    disabled={busy || loadingMore}
                  >
                    {loadingMore && (
                      <Loader2 className="h-4 w-4 animate-spin" />
                    )}
                    {t("contacts.loadMore")} (
                    {t("contacts.showing", { count: items.length, total })})
                  </Button>
                </div>
              )}
            </div>
          )}
        </Card>

        <Card className="min-h-0 overflow-y-auto p-4">
          {selected ? (
            <ContactDetails
              contact={selected}
              groups={groups}
              onEdit={() => setEditing(selected)}
              onDelete={() => void removeContact(selected)}
              onFavorite={() => void toggleFavorite(selected)}
              onAvatar={(file) => void uploadAvatar(selected, file)}
              onClearAvatar={() => void clearAvatar(selected)}
            />
          ) : (
            <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
              {t("contacts.selectPrompt")}
            </div>
          )}
        </Card>
      </div>

      <ContactEditor
        open={editing !== null}
        contact={editing}
        groups={groups}
        busy={busy}
        onClose={() => setEditing(null)}
        onSave={(payload) => void saveContact(payload)}
      />
      <GroupEditor
        value={groupEditor}
        busy={busy}
        onClose={() => setGroupEditor(null)}
        onSaved={async () => {
          setGroupEditor(null);
          await refreshGroups();
          await fetchPage(0, true);
        }}
      />
    </PageWrapper>
  );
}

function Banner({
  tone,
  message,
  onClose,
}: {
  tone: "error" | "success";
  message: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div
      className={cn(
        "flex items-center justify-between rounded-md border px-3 py-2 text-sm",
        tone === "error"
          ? "border-destructive/40 bg-destructive/10 text-destructive"
          : "border-emerald-500/40 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
      )}
    >
      <span>{message}</span>
      <button
        onClick={onClose}
        aria-label={t("common.close")}
        className="ml-2 shrink-0"
      >
        <X className="h-4 w-4" />
      </button>
    </div>
  );
}

function FilterButton({
  active,
  icon,
  label,
  count,
  onClick,
}: {
  active: boolean;
  icon: ReactNode;
  label: string;
  count?: number;
  onClick: () => void;
}) {
  return (
    <button
      className={cn(
        "flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-sm hover:bg-accent",
        active && "bg-accent font-medium",
      )}
      onClick={onClick}
    >
      {icon}
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {count !== undefined && (
        <span className="text-xs text-muted-foreground">{count}</span>
      )}
    </button>
  );
}

function EmptyContacts({ onCreate }: { onCreate: () => void }) {
  const { t } = useTranslation();
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-8 text-center">
      <UserRound className="h-8 w-8 text-muted-foreground" />
      <div>
        <p className="text-sm font-medium">{t("contacts.empty")}</p>
        <p className="mt-1 text-sm text-muted-foreground">
          {t("contacts.emptyHint")}
        </p>
      </div>
      <Button size="sm" onClick={onCreate}>
        <Plus className="h-4 w-4" />
        {t("contacts.addContact")}
      </Button>
    </div>
  );
}

function ContactDetails({
  contact,
  groups,
  onEdit,
  onDelete,
  onFavorite,
  onAvatar,
  onClearAvatar,
}: {
  contact: ContactRecord;
  groups: ContactGroup[];
  onEdit: () => void;
  onDelete: () => void;
  onFavorite: () => void;
  onAvatar: (file?: File) => void;
  onClearAvatar: () => void;
}) {
  const { t } = useTranslation();
  const groupMap = useMemo(
    () => new Map(groups.map((group) => [group.id, group])),
    [groups],
  );
  const avatarInput = useRef<HTMLInputElement>(null);
  function changeAvatar(e: ChangeEvent<HTMLInputElement>) {
    onAvatar(e.target.files?.[0]);
    e.target.value = "";
  }
  return (
    <div className="space-y-5" role="region" aria-label={t("contacts.title")}>
      <div className="flex items-start gap-3">
        <ContactAvatar contact={contact} large />
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-lg font-semibold">
            {displayName(contact) || t("contacts.noName")}
          </h2>
          <p className="text-sm text-muted-foreground">{subtitle(contact)}</p>
        </div>
      </div>
      <div className="flex flex-wrap gap-2">
        <input
          ref={avatarInput}
          type="file"
          accept="image/*"
          className="hidden"
          onChange={changeAvatar}
          aria-label={t("contacts.photo")}
        />
        <Button
          variant="outline"
          size="sm"
          onClick={() => avatarInput.current?.click()}
        >
          <Upload className="h-4 w-4" />
          {t("contacts.photo")}
        </Button>
        {contact.avatar_url && (
          <Button variant="ghost" size="sm" onClick={onClearAvatar}>
            <X className="h-4 w-4" />
          </Button>
        )}
        <Button
          variant="outline"
          size="sm"
          onClick={onFavorite}
          aria-pressed={contact.is_favorite}
        >
          <Star
            className={cn(
              "h-4 w-4",
              contact.is_favorite && "fill-current text-amber-500",
            )}
          />
          {t("contacts.favorite")}
        </Button>
        <Button variant="outline" size="sm" onClick={onEdit}>
          <Pencil className="h-4 w-4" />
          {t("contacts.edit")}
        </Button>
        <Button variant="ghost" size="sm" onClick={onDelete}>
          <Trash2 className="h-4 w-4" />
        </Button>
      </div>

      <DetailSection
        icon={<Mail className="h-4 w-4" />}
        title={t("contacts.email")}
      >
        {contact.emails.length ? (
          contact.emails.map((email) => (
            <DetailLine key={`e-${email.value}`} label={email.type}>
              <a className="hover:underline" href={`mailto:${email.value}`}>
                {email.value}
              </a>
              <CopyButton value={email.value} />
            </DetailLine>
          ))
        ) : (
          <Muted>{t("contacts.noEmail")}</Muted>
        )}
      </DetailSection>

      <DetailSection
        icon={<Phone className="h-4 w-4" />}
        title={t("contacts.phone")}
      >
        {contact.phones.length ? (
          contact.phones.map((phone) => (
            <DetailLine key={`p-${phone.value}`} label={phone.type}>
              <a className="hover:underline" href={`tel:${phone.value}`}>
                {phone.value}
              </a>
              <CopyButton value={phone.value} />
            </DetailLine>
          ))
        ) : (
          <Muted>{t("contacts.noPhone")}</Muted>
        )}
      </DetailSection>

      {contact.addresses.length > 0 && (
        <DetailSection
          icon={<MapPin className="h-4 w-4" />}
          title={t("contacts.addresses")}
        >
          {contact.addresses.map((addr, idx) => (
            <DetailLine key={`a-${idx}`} label={addr.type}>
              <span className="break-words">
                {[
                  addr.street,
                  [addr.locality, addr.region].filter(Boolean).join(" "),
                  addr.postal_code,
                  addr.country,
                ]
                  .filter(Boolean)
                  .join(", ")}
              </span>
            </DetailLine>
          ))}
        </DetailSection>
      )}

      {contact.urls.length > 0 && (
        <DetailSection
          icon={<Globe className="h-4 w-4" />}
          title={t("contacts.urls")}
        >
          {contact.urls.map((url, idx) => (
            <DetailLine key={`u-${idx}`}>
              <a
                className="hover:underline"
                href={url.value}
                target="_blank"
                rel="noreferrer"
              >
                {url.value}
              </a>
            </DetailLine>
          ))}
        </DetailSection>
      )}

      {contact.ims.length > 0 && (
        <DetailSection
          icon={<MessageCircle className="h-4 w-4" />}
          title={t("contacts.ims")}
        >
          {contact.ims.map((im, idx) => (
            <DetailLine key={`i-${idx}`} label={im.type}>
              {im.value}
            </DetailLine>
          ))}
        </DetailSection>
      )}

      {contact.group_ids.length > 0 && (
        <DetailSection
          icon={<Tags className="h-4 w-4" />}
          title={t("contacts.groupLabel")}
        >
          <div className="flex flex-wrap gap-1.5">
            {contact.group_ids.map((gid) => {
              const group = groupMap.get(gid);
              if (!group) return null;
              return (
                <Badge key={gid} variant="outline">
                  {group.name}
                </Badge>
              );
            })}
          </div>
        </DetailSection>
      )}

      {(contact.birthday || contact.notes) && (
        <DetailSection
          icon={<UserRound className="h-4 w-4" />}
          title={t("contacts.notes")}
        >
          {contact.birthday && (
            <DetailLine label={t("contacts.birthday")}>
              {contact.birthday}
            </DetailLine>
          )}
          {contact.notes && (
            <p className="whitespace-pre-wrap text-sm">{contact.notes}</p>
          )}
        </DetailSection>
      )}
    </div>
  );
}

function DetailSection({
  icon,
  title,
  children,
}: {
  icon: ReactNode;
  title: string;
  children: ReactNode;
}) {
  return (
    <section className="space-y-2">
      <h3 className="flex items-center gap-2 text-xs font-semibold uppercase text-muted-foreground">
        {icon}
        {title}
      </h3>
      <div className="space-y-2">{children}</div>
    </section>
  );
}

function DetailLine({
  label,
  children,
}: {
  label?: string;
  children: ReactNode;
}) {
  return (
    <div className="flex items-center gap-2 text-sm">
      {label && (
        <span className="w-14 shrink-0 text-xs capitalize text-muted-foreground">
          {label}
        </span>
      )}
      <span className="min-w-0 break-words">{children}</span>
    </div>
  );
}

function Muted({ children }: { children: ReactNode }) {
  return <p className="text-sm text-muted-foreground">{children}</p>;
}

function CopyButton({ value }: { value: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // ignore — clipboard may be unavailable
    }
  }
  return (
    <button
      type="button"
      onClick={copy}
      className="text-muted-foreground hover:text-foreground"
      aria-label={copied ? t("contacts.copied") : t("contacts.copy")}
      title={copied ? t("contacts.copied") : t("contacts.copy")}
    >
      {copied ? (
        <Check className="h-3.5 w-3.5 text-emerald-500" />
      ) : (
        <Copy className="h-3.5 w-3.5" />
      )}
    </button>
  );
}

function ContactEditor({
  open,
  contact,
  groups,
  busy,
  onClose,
  onSave,
}: {
  open: boolean;
  contact: ContactRecord | null;
  groups: ContactGroup[];
  busy: boolean;
  onClose: () => void;
  onSave: (payload: ContactPayload) => void;
}) {
  const { t } = useTranslation();
  const [form, setForm] = useState<ContactFormState>(() => contactToForm(null));
  const [formError, setFormError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setForm(contactToForm(contact));
    setFormError(null);
  }, [contact, open]);

  function set<K extends keyof ContactFormState>(
    key: K,
    value: ContactFormState[K],
  ) {
    setForm((prev) => ({ ...prev, [key]: value }));
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    if (!hasIdentity(form)) {
      setFormError(t("contacts.nameRequired"));
      return;
    }
    onSave(formToPayload(form));
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={contact?.id ? t("contacts.editContact") : t("contacts.newContact")}
      className="max-w-2xl"
    >
      <form onSubmit={submit} className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t("contacts.displayName")}>
            <Input
              value={form.displayName}
              onChange={(e) => set("displayName", e.target.value)}
              placeholder="Ada Lovelace"
            />
          </Field>
          <Field label={t("contacts.nickname")}>
            <Input
              value={form.nickname}
              onChange={(e) => set("nickname", e.target.value)}
            />
          </Field>
          <Field label={t("contacts.namePrefix")}>
            <Input
              value={form.namePrefix}
              onChange={(e) => set("namePrefix", e.target.value)}
            />
          </Field>
          <Field label={t("contacts.givenName")}>
            <Input
              value={form.givenName}
              onChange={(e) => set("givenName", e.target.value)}
            />
          </Field>
          <Field label={t("contacts.middleName")}>
            <Input
              value={form.middleName}
              onChange={(e) => set("middleName", e.target.value)}
            />
          </Field>
          <Field label={t("contacts.familyName")}>
            <Input
              value={form.familyName}
              onChange={(e) => set("familyName", e.target.value)}
            />
          </Field>
          <Field label={t("contacts.nameSuffix")}>
            <Input
              value={form.nameSuffix}
              onChange={(e) => set("nameSuffix", e.target.value)}
            />
          </Field>
          <Field label={t("contacts.company")}>
            <Input
              value={form.company}
              onChange={(e) => set("company", e.target.value)}
            />
          </Field>
          <Field label={t("contacts.jobTitle")}>
            <Input
              value={form.title}
              onChange={(e) => set("title", e.target.value)}
            />
          </Field>
          <Field label={t("contacts.department")}>
            <Input
              value={form.department}
              onChange={(e) => set("department", e.target.value)}
            />
          </Field>
          <Field label={t("contacts.birthday")}>
            <Input
              type="date"
              value={form.birthday}
              onChange={(e) => set("birthday", e.target.value)}
            />
          </Field>
        </div>

        <ValueListEditor
          label={t("contacts.emails")}
          placeholder="name@example.com"
          values={form.emails}
          onChange={(values) => set("emails", values)}
        />
        <ValueListEditor
          label={t("contacts.phone")}
          placeholder="+82 10 0000 0000"
          values={form.phones}
          onChange={(values) => set("phones", values)}
        />
        <ValueListEditor
          label={t("contacts.ims")}
          placeholder="user@example"
          values={form.ims}
          onChange={(values) => set("ims", values)}
        />
        <ValueListEditor
          label={t("contacts.urls")}
          placeholder="https://"
          values={form.urls}
          onChange={(values) => set("urls", values)}
        />
        <AddressEditor
          values={form.addresses}
          onChange={(values) => set("addresses", values)}
        />

        {groups.length > 0 && (
          <div className="space-y-2">
            <Label>{t("contacts.groupLabel")}</Label>
            <div className="flex flex-wrap gap-2">
              {groups.map((group) => {
                const active = form.groupIDs.includes(group.id);
                return (
                  <button
                    key={group.id}
                    type="button"
                    onClick={() =>
                      set(
                        "groupIDs",
                        active
                          ? form.groupIDs.filter((id) => id !== group.id)
                          : [...form.groupIDs, group.id],
                      )
                    }
                    className={cn(
                      "rounded-md border px-2.5 py-1.5 text-sm hover:bg-accent",
                      active && "border-primary bg-primary/10",
                    )}
                  >
                    {group.name}
                  </button>
                );
              })}
            </div>
          </div>
        )}
        <Field label={t("contacts.notes")}>
          <Textarea
            value={form.notes}
            onChange={(e) => set("notes", e.target.value)}
            rows={4}
          />
        </Field>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={form.isFavorite}
            onChange={(e) => set("isFavorite", e.target.checked)}
          />
          {t("contacts.addToFavorites")}
        </label>
        {formError && <p className="text-sm text-destructive">{formError}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="outline" onClick={onClose}>
            {t("contacts.cancel")}
          </Button>
          <Button type="submit" disabled={busy}>
            {busy && <Loader2 className="h-4 w-4 animate-spin" />}
            {t("contacts.save")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function GroupEditor({
  value,
  busy,
  onClose,
  onSaved,
}: {
  value: ContactGroup | "new" | null;
  busy: boolean;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const [name, setName] = useState("");
  const [color, setColor] = useState("#2563eb");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!value) return;
    setName(value === "new" ? "" : value.name);
    setColor(value === "new" ? "#2563eb" : value.color || "#2563eb");
    setError(null);
  }, [value]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (saving) return;
    setSaving(true);
    setError(null);
    try {
      if (value === "new") await contacts.createGroup({ name, color });
      else if (value) await contacts.updateGroup(value.id, { name, color });
      await onSaved();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    if (!value || value === "new") return;
    if (saving) return;
    if (
      !(await confirm({
        title: t("contacts.confirmDeleteGroup", { name: value.name }),
        destructive: true,
        confirmLabel: t("common.delete"),
        cancelLabel: t("common.cancel"),
      }))
    )
      return;
    setSaving(true);
    try {
      await contacts.removeGroup(value.id);
      await onSaved();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      open={value !== null}
      onClose={onClose}
      title={value === "new" ? t("contacts.newGroup") : t("contacts.editGroup")}
    >
      <form onSubmit={submit} className="space-y-4">
        <Field label={t("contacts.groupName")}>
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label={t("contacts.color")}>
          <div className="flex items-center gap-2">
            <input
              type="color"
              value={color}
              onChange={(e) => setColor(e.target.value)}
              className="h-10 w-12 rounded-md border bg-background"
              aria-label={t("contacts.color")}
            />
            <Input value={color} onChange={(e) => setColor(e.target.value)} />
          </div>
        </Field>
        {error && <p className="text-sm text-destructive">{error}</p>}
        <div className="flex justify-between gap-2 pt-2">
          {value !== "new" && (
            <Button
              type="button"
              variant="ghost"
              onClick={() => void remove()}
              disabled={busy || saving}
            >
              <Trash2 className="h-4 w-4" />
              {t("contacts.deleteGroup")}
            </Button>
          )}
          <div className="ml-auto flex gap-2">
            <Button
              type="button"
              variant="outline"
              onClick={onClose}
              disabled={busy || saving}
            >
              {t("contacts.cancel")}
            </Button>
            <Button type="submit" disabled={busy || saving}>
              {saving && <Loader2 className="h-4 w-4 animate-spin" />}
              {t("contacts.save")}
            </Button>
          </div>
        </div>
      </form>
    </Modal>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="space-y-2">
      <Label>{label}</Label>
      {children}
    </div>
  );
}

function ValueListEditor({
  label,
  placeholder,
  values,
  onChange,
}: {
  label: string;
  placeholder: string;
  values: FormValue[];
  onChange: (values: FormValue[]) => void;
}) {
  const { t } = useTranslation();
  const rows = values.length ? values : [newFormValue()];

  function update(id: string, patch: Partial<FormValue>) {
    onChange(rows.map((row) => (row.id === id ? { ...row, ...patch } : row)));
  }

  function remove(id: string) {
    const next = rows.filter((row) => row.id !== id);
    onChange(next.length ? next : [newFormValue()]);
  }

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <Label>{label}</Label>
        <button
          type="button"
          className="rounded p-1 text-muted-foreground hover:bg-accent"
          onClick={() => onChange([...rows, newFormValue()])}
          aria-label={`${label} ${t("contacts.addValue")}`}
        >
          <Plus className="h-3.5 w-3.5" />
        </button>
      </div>
      <div className="space-y-2">
        {rows.map((row) => (
          <div key={row.id} className="grid grid-cols-[1fr_110px_32px] gap-2">
            <Input
              value={row.value}
              onChange={(e) => update(row.id, { value: e.target.value })}
              placeholder={placeholder}
            />
            <Input
              value={row.type ?? ""}
              onChange={(e) => update(row.id, { type: e.target.value })}
              placeholder={t("contacts.type")}
            />
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="h-10 w-8"
              onClick={() => remove(row.id)}
              disabled={rows.length === 1 && !row.value}
              aria-label={`${label} ${t("contacts.delete")}`}
            >
              <Trash2 className="h-4 w-4" />
            </Button>
          </div>
        ))}
      </div>
    </div>
  );
}

const ADDRESS_TYPES = ["", "home", "work", "other"];

function AddressEditor({
  values,
  onChange,
}: {
  values: FormAddress[];
  onChange: (values: FormAddress[]) => void;
}) {
  const { t } = useTranslation();
  const rows = values;

  function update(id: string, patch: Partial<FormAddress>) {
    onChange(rows.map((row) => (row.id === id ? { ...row, ...patch } : row)));
  }

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <Label>{t("contacts.addresses")}</Label>
        <button
          type="button"
          className="rounded p-1 text-muted-foreground hover:bg-accent"
          onClick={() => onChange([...rows, newFormAddress()])}
          aria-label={`${t("contacts.addresses")} ${t("contacts.addValue")}`}
        >
          <Plus className="h-3.5 w-3.5" />
        </button>
      </div>
      {rows.map((row) => (
        <div key={row.id} className="space-y-2 rounded-md border p-3">
          <div className="grid grid-cols-[110px_1fr_32px] gap-2">
            <select
              className="h-10 rounded-md border bg-background px-2 text-sm"
              value={row.type ?? ""}
              onChange={(e) => update(row.id, { type: e.target.value })}
              aria-label={t("contacts.type")}
            >
              {ADDRESS_TYPES.map((type) => (
                <option key={type} value={type}>
                  {type === "home"
                    ? t("contacts.addressTypeHome")
                    : type === "work"
                      ? t("contacts.addressTypeWork")
                      : type === "other"
                        ? t("contacts.addressTypeOther")
                        : t("contacts.type")}
                </option>
              ))}
            </select>
            <Input
              value={row.street ?? ""}
              onChange={(e) => update(row.id, { street: e.target.value })}
              placeholder={t("contacts.street")}
            />
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="h-10 w-8"
              onClick={() => onChange(rows.filter((r) => r.id !== row.id))}
              aria-label={`${t("contacts.addresses")} ${t("contacts.delete")}`}
            >
              <Trash2 className="h-4 w-4" />
            </Button>
          </div>
          <div className="grid gap-2 sm:grid-cols-2">
            <Input
              value={row.locality ?? ""}
              onChange={(e) => update(row.id, { locality: e.target.value })}
              placeholder={t("contacts.locality")}
            />
            <Input
              value={row.region ?? ""}
              onChange={(e) => update(row.id, { region: e.target.value })}
              placeholder={t("contacts.region")}
            />
            <Input
              value={row.postal_code ?? ""}
              onChange={(e) => update(row.id, { postal_code: e.target.value })}
              placeholder={t("contacts.postalCode")}
            />
            <Input
              value={row.country ?? ""}
              onChange={(e) => update(row.id, { country: e.target.value })}
              placeholder={t("contacts.country")}
            />
          </div>
        </div>
      ))}
    </div>
  );
}

function ContactAvatar({
  contact,
  large = false,
}: {
  contact: ContactRecord;
  large?: boolean;
}) {
  return (
    <Avatar className={large ? "h-14 w-14" : "h-10 w-10"}>
      {contact.avatar_url && <AvatarImage src={contact.avatar_url} />}
      <AvatarFallback>{initials(contact)}</AvatarFallback>
    </Avatar>
  );
}
