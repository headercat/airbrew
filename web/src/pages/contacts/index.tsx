import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import {
  Download,
  Loader2,
  Mail,
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
import { Textarea } from "@/components/ui/textarea";
import { PageHeader, PageWrapper } from "@/components/page";
import {
  contacts,
  type ContactGroup,
  type ContactPayload,
  type ContactRecord,
  type ContactValue,
} from "@/lib/contacts";
import { cn } from "@/lib/utils";
import {
  blankForm,
  contactToForm,
  displayName,
  emptyContact,
  errorMessage,
  formToPayload,
  initials,
  subtitle,
  type ContactFormState,
} from "./helpers";

export default function ContactsPage() {
  const [status, setStatus] = useState<{ enabled: boolean } | null>(null);
  const [items, setItems] = useState<ContactRecord[]>([]);
  const [groups, setGroups] = useState<ContactGroup[]>([]);
  const [selectedID, setSelectedID] = useState("");
  const [query, setQuery] = useState("");
  const [activeGroup, setActiveGroup] = useState("");
  const [favoritesOnly, setFavoritesOnly] = useState(false);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState<ContactRecord | null>(null);
  const [groupEditor, setGroupEditor] = useState<ContactGroup | "new" | null>(
    null,
  );
  const fileInput = useRef<HTMLInputElement>(null);

  const selected = useMemo(
    () => items.find((item) => item.id === selectedID) ?? items[0] ?? null,
    [items, selectedID],
  );
  const groupMap = useMemo(
    () => new Map(groups.map((group) => [group.id, group])),
    [groups],
  );

  const refreshGroups = useCallback(async () => {
    const res = await contacts.groups();
    setGroups(res.groups ?? []);
  }, []);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [list] = await Promise.all([
        contacts.list({
          q: query,
          group: activeGroup,
          favorite: favoritesOnly,
          sort: "name",
          limit: 100,
        }),
        refreshGroups(),
      ]);
      setItems(list.contacts ?? []);
      setSelectedID((current) => {
        if (current && list.contacts?.some((item) => item.id === current)) {
          return current;
        }
        return list.contacts?.[0]?.id ?? "";
      });
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setLoading(false);
    }
  }, [activeGroup, favoritesOnly, query, refreshGroups]);

  useEffect(() => {
    contacts
      .status()
      .then((s) =>
        setStatus({
          enabled:
            (s.enabled as unknown) !== false && String(s.enabled) !== "false",
        }),
      )
      .catch(() => setStatus({ enabled: true }));
  }, []);

  useEffect(() => {
    const handle = window.setTimeout(() => void refresh(), 180);
    return () => window.clearTimeout(handle);
  }, [refresh]);

  async function saveContact(payload: ContactPayload) {
    setBusy(true);
    setError(null);
    try {
      const saved = editing
        ? await contacts.update(editing.id, payload)
        : await contacts.create(payload);
      setEditing(null);
      await refresh();
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
      setItems((prev) => prev.map((row) => (row.id === next.id ? next : row)));
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  async function removeContact(item: ContactRecord) {
    if (!confirm(`${displayName(item)} 연락처를 삭제할까요?`)) return;
    setBusy(true);
    try {
      await contacts.remove(item.id);
      await refresh();
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
      await refresh();
      setError(`${res.imported}개 연락처를 가져왔습니다.`);
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
        <PageHeader title="주소록" />
        <Card className="p-6 text-sm text-muted-foreground">
          주소록 모듈이 비활성화되어 있습니다.
        </Card>
      </PageWrapper>
    );
  }

  return (
    <PageWrapper className="h-full overflow-hidden">
      <PageHeader
        title="주소록"
        description="연락처, 그룹, 즐겨찾기와 vCard 가져오기/내보내기를 관리합니다."
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
              title="vCard 가져오기"
            >
              <Upload className="h-4 w-4" />
              가져오기
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => void exportFile()}
              disabled={busy}
              title="vCard 내보내기"
            >
              <Download className="h-4 w-4" />
              내보내기
            </Button>
            <Button size="sm" onClick={() => setEditing(emptyContact())}>
              <Plus className="h-4 w-4" />
              연락처
            </Button>
          </div>
        }
      />

      {error && (
        <div className="flex items-center justify-between rounded-md border bg-muted/40 px-3 py-2 text-sm">
          <span>{error}</span>
          <button onClick={() => setError(null)} aria-label="닫기">
            <X className="h-4 w-4" />
          </button>
        </div>
      )}

      <div className="grid min-h-[620px] gap-4 lg:grid-cols-[260px_minmax(320px,1fr)_320px]">
        <Card className="flex min-h-0 flex-col p-3">
          <div className="relative mb-3">
            <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="이름, 회사, 이메일 검색"
              className="pl-9"
            />
          </div>
          <div className="space-y-1">
            <FilterButton
              active={!activeGroup && !favoritesOnly}
              icon={<Users className="h-4 w-4" />}
              label="모든 연락처"
              count={items.length}
              onClick={() => {
                setActiveGroup("");
                setFavoritesOnly(false);
              }}
            />
            <FilterButton
              active={favoritesOnly}
              icon={<Star className="h-4 w-4" />}
              label="즐겨찾기"
              onClick={() => {
                setActiveGroup("");
                setFavoritesOnly(true);
              }}
            />
          </div>
          <div className="mt-4 flex items-center justify-between px-1 text-xs font-medium uppercase text-muted-foreground">
            <span>그룹</span>
            <button
              className="rounded p-1 hover:bg-accent"
              onClick={() => setGroupEditor("new")}
              aria-label="그룹 추가"
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
                    setActiveGroup(group.id);
                    setFavoritesOnly(false);
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
                  aria-label="그룹 편집"
                >
                  <Pencil className="h-3.5 w-3.5" />
                </button>
              </div>
            ))}
          </div>
        </Card>

        <Card className="min-h-0 overflow-hidden p-0">
          {loading ? (
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
                  className={cn(
                    "flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-accent/60",
                    selected?.id === item.id && "bg-accent",
                  )}
                >
                  <ContactAvatar contact={item} />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <p className="truncate text-sm font-medium">
                        {displayName(item)}
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
            />
          ) : (
            <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
              연락처를 선택하세요.
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
          await refresh();
        }}
      />
    </PageWrapper>
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
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-8 text-center">
      <UserRound className="h-8 w-8 text-muted-foreground" />
      <div>
        <p className="text-sm font-medium">아직 연락처가 없습니다.</p>
        <p className="mt-1 text-sm text-muted-foreground">
          직접 추가하거나 vCard 파일을 가져와 시작하세요.
        </p>
      </div>
      <Button size="sm" onClick={onCreate}>
        <Plus className="h-4 w-4" />
        연락처 추가
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
}: {
  contact: ContactRecord;
  groups: ContactGroup[];
  onEdit: () => void;
  onDelete: () => void;
  onFavorite: () => void;
}) {
  const groupMap = new Map(groups.map((group) => [group.id, group]));
  return (
    <div className="space-y-5">
      <div className="flex items-start gap-3">
        <ContactAvatar contact={contact} large />
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-lg font-semibold">
            {displayName(contact)}
          </h2>
          <p className="text-sm text-muted-foreground">{subtitle(contact)}</p>
        </div>
      </div>
      <div className="flex gap-2">
        <Button variant="outline" size="sm" onClick={onFavorite}>
          <Star
            className={cn(
              "h-4 w-4",
              contact.is_favorite && "fill-current text-amber-500",
            )}
          />
          즐겨찾기
        </Button>
        <Button variant="outline" size="sm" onClick={onEdit}>
          <Pencil className="h-4 w-4" />
          편집
        </Button>
        <Button variant="ghost" size="sm" onClick={onDelete}>
          <Trash2 className="h-4 w-4" />
        </Button>
      </div>
      <DetailSection icon={<Mail className="h-4 w-4" />} title="이메일">
        {contact.emails.length ? (
          contact.emails.map((email, idx) => (
            <DetailLine key={`${email.value}-${idx}`} label={email.type}>
              <a className="hover:underline" href={`mailto:${email.value}`}>
                {email.value}
              </a>
            </DetailLine>
          ))
        ) : (
          <Muted>등록된 이메일이 없습니다.</Muted>
        )}
      </DetailSection>
      <DetailSection icon={<Phone className="h-4 w-4" />} title="전화">
        {contact.phones.length ? (
          contact.phones.map((phone, idx) => (
            <DetailLine key={`${phone.value}-${idx}`} label={phone.type}>
              <a className="hover:underline" href={`tel:${phone.value}`}>
                {phone.value}
              </a>
            </DetailLine>
          ))
        ) : (
          <Muted>등록된 전화번호가 없습니다.</Muted>
        )}
      </DetailSection>
      {contact.group_ids.length > 0 && (
        <DetailSection icon={<Tags className="h-4 w-4" />} title="그룹">
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
        <DetailSection icon={<UserRound className="h-4 w-4" />} title="메모">
          {contact.birthday && (
            <DetailLine label="생일">{contact.birthday}</DetailLine>
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
        <span className="w-14 shrink-0 text-xs text-muted-foreground">
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
  const [form, setForm] = useState<ContactFormState>(blankForm);

  useEffect(() => {
    if (!open) return;
    setForm(contactToForm(contact));
  }, [contact, open]);

  function set<K extends keyof ContactFormState>(
    key: K,
    value: ContactFormState[K],
  ) {
    setForm((prev) => ({ ...prev, [key]: value }));
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    onSave(formToPayload(form));
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={contact?.id ? "연락처 편집" : "연락처 추가"}
      className="max-w-2xl"
    >
      <form onSubmit={submit} className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="표시 이름">
            <Input
              value={form.displayName}
              onChange={(e) => set("displayName", e.target.value)}
              placeholder="Ada Lovelace"
            />
          </Field>
          <Field label="별명">
            <Input
              value={form.nickname}
              onChange={(e) => set("nickname", e.target.value)}
            />
          </Field>
          <Field label="이름">
            <Input
              value={form.givenName}
              onChange={(e) => set("givenName", e.target.value)}
            />
          </Field>
          <Field label="성">
            <Input
              value={form.familyName}
              onChange={(e) => set("familyName", e.target.value)}
            />
          </Field>
          <Field label="회사">
            <Input
              value={form.company}
              onChange={(e) => set("company", e.target.value)}
            />
          </Field>
          <Field label="직함">
            <Input
              value={form.title}
              onChange={(e) => set("title", e.target.value)}
            />
          </Field>
          <ValueListEditor
            label="이메일"
            placeholder="name@example.com"
            values={form.emails}
            onChange={(values) => set("emails", values)}
          />
          <ValueListEditor
            label="전화"
            placeholder="+82 10 0000 0000"
            values={form.phones}
            onChange={(values) => set("phones", values)}
          />
          <Field label="생일">
            <Input
              type="date"
              value={form.birthday}
              onChange={(e) => set("birthday", e.target.value)}
            />
          </Field>
        </div>
        {groups.length > 0 && (
          <div className="space-y-2">
            <Label>그룹</Label>
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
        <Field label="메모">
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
          즐겨찾기에 추가
        </label>
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="outline" onClick={onClose}>
            취소
          </Button>
          <Button type="submit" disabled={busy}>
            {busy && <Loader2 className="h-4 w-4 animate-spin" />}
            저장
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
  const [name, setName] = useState("");
  const [color, setColor] = useState("#2563eb");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!value) return;
    setName(value === "new" ? "" : value.name);
    setColor(value === "new" ? "#2563eb" : value.color || "#2563eb");
    setError(null);
  }, [value]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      if (value === "new") await contacts.createGroup({ name, color });
      else if (value) await contacts.updateGroup(value.id, { name, color });
      await onSaved();
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  async function remove() {
    if (!value || value === "new") return;
    if (
      !confirm(`${value.name} 그룹을 삭제할까요? 연락처는 삭제되지 않습니다.`)
    ) {
      return;
    }
    try {
      await contacts.removeGroup(value.id);
      await onSaved();
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  return (
    <Modal
      open={value !== null}
      onClose={onClose}
      title={value === "new" ? "그룹 추가" : "그룹 편집"}
    >
      <form onSubmit={submit} className="space-y-4">
        <Field label="이름">
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="색상">
          <div className="flex items-center gap-2">
            <input
              type="color"
              value={color}
              onChange={(e) => setColor(e.target.value)}
              className="h-10 w-12 rounded-md border bg-background"
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
              disabled={busy}
            >
              <Trash2 className="h-4 w-4" />
              삭제
            </Button>
          )}
          <div className="ml-auto flex gap-2">
            <Button type="button" variant="outline" onClick={onClose}>
              취소
            </Button>
            <Button type="submit" disabled={busy}>
              저장
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
  values: ContactValue[];
  onChange: (values: ContactValue[]) => void;
}) {
  const rows = values.length ? values : [{ value: "", type: "" }];

  function update(index: number, patch: Partial<ContactValue>) {
    onChange(rows.map((row, i) => (i === index ? { ...row, ...patch } : row)));
  }

  function remove(index: number) {
    const next = rows.filter((_, i) => i !== index);
    onChange(next.length ? next : [{ value: "", type: "" }]);
  }

  return (
    <div className="space-y-2 sm:col-span-2">
      <div className="flex items-center justify-between">
        <Label>{label}</Label>
        <button
          type="button"
          className="rounded p-1 text-muted-foreground hover:bg-accent"
          onClick={() => onChange([...rows, { value: "", type: "" }])}
          aria-label={`${label} 추가`}
        >
          <Plus className="h-3.5 w-3.5" />
        </button>
      </div>
      <div className="space-y-2">
        {rows.map((row, index) => (
          <div key={index} className="grid grid-cols-[1fr_96px_32px] gap-2">
            <Input
              value={row.value}
              onChange={(e) => update(index, { value: e.target.value })}
              placeholder={placeholder}
            />
            <Input
              value={row.type ?? ""}
              onChange={(e) => update(index, { type: e.target.value })}
              placeholder="type"
            />
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="h-10 w-8"
              onClick={() => remove(index)}
              disabled={rows.length === 1 && !row.value}
              aria-label={`${label} 삭제`}
            >
              <Trash2 className="h-4 w-4" />
            </Button>
          </div>
        ))}
      </div>
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
