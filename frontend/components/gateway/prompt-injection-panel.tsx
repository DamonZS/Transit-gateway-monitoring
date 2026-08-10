import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  KeyRound,
  MessageSquareText,
  Plus,
  Save,
  Trash2,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { cn } from "@/lib/utils"
import type { GatewayKey, GatewaySystemPromptRule } from "@/lib/api-types"

type PromptInjectionPanelProps = {
  rules: GatewaySystemPromptRule[]
  keys: GatewayKey[]
  busy: boolean
  onChange: (rules: GatewaySystemPromptRule[]) => void
  onSave: () => void
}

const emptyRule = (): GatewaySystemPromptRule => ({
  enabled: true,
  text: "",
  override: false,
  key_scope: "all",
  key_ids: [],
})

function selectedKeyIDs(rule: GatewaySystemPromptRule) {
  return [...new Set(rule.key_ids.filter((keyID) => Number.isInteger(keyID) && keyID > 0))]
}

export function PromptInjectionPanel({
  rules,
  keys,
  busy,
  onChange,
  onSave,
}: PromptInjectionPanelProps) {
  function updateRule(index: number, patch: Partial<GatewaySystemPromptRule>) {
    onChange(rules.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)))
  }

  function moveRule(index: number, offset: -1 | 1) {
    const target = index + offset
    if (target < 0 || target >= rules.length) return
    const next = [...rules]
    ;[next[index], next[target]] = [next[target], next[index]]
    onChange(next)
  }

  function updateSelectedKeys(index: number, keyIDs: number[]) {
    updateRule(index, {
      key_scope: "selected",
      key_ids: [...new Set(keyIDs)],
    })
  }

  return (
    <div className="overflow-hidden rounded-lg border border-border bg-card shadow-none">
      <div className="flex flex-wrap items-start justify-between gap-3 border-b border-border px-4 py-4 sm:px-5">
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-sm font-medium">
            <MessageSquareText className="size-4 text-primary" />
            系统提示词注入
          </div>
          <p className="mt-1 text-xs leading-5 text-muted-foreground">
            多条规则按从上到下的顺序注入，指定 Key 支持多选。
          </p>
        </div>
        <div className="inline-flex shrink-0 items-center rounded-md border border-border bg-muted/35 px-2 py-1 font-mono text-[11px] text-muted-foreground">
          system
        </div>
      </div>

      <div className="divide-y divide-border">
        {rules.map((rule, index) => {
          const scopedKey = rule.key_scope === "selected"
          const selectedKeys = selectedKeyIDs(rule)
          const allKeysSelected = keys.length > 0 && selectedKeys.length === keys.length

          return (
            <section key={index} className={cn("space-y-4 p-4 sm:p-5", !rule.enabled && "opacity-65")}>
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div className="flex items-center gap-2">
                  <span className="inline-flex size-6 items-center justify-center rounded-md bg-primary/10 text-xs font-semibold text-primary">
                    {index + 1}
                  </span>
                  <span className="text-sm font-medium">提示词 #{index + 1}</span>
                  <Switch
                    checked={rule.enabled}
                    disabled={busy}
                    aria-label={`启用提示词 ${index + 1}`}
                    onCheckedChange={(enabled) => updateRule(index, { enabled })}
                  />
                </div>
                <div className="flex items-center gap-1">
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    title="上移"
                    disabled={busy || index === 0}
                    onClick={() => moveRule(index, -1)}
                  >
                    <ArrowUp className="size-3.5" />
                  </Button>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    title="下移"
                    disabled={busy || index === rules.length - 1}
                    onClick={() => moveRule(index, 1)}
                  >
                    <ArrowDown className="size-3.5" />
                  </Button>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    title="删除提示词"
                    disabled={busy}
                    onClick={() => onChange(rules.filter((_, i) => i !== index))}
                  >
                    <Trash2 className="size-3.5 text-muted-foreground" />
                  </Button>
                </div>
              </div>

              <div className="space-y-1.5">
                <Label htmlFor={`gateway-system-prompt-${index}`}>提示词内容</Label>
                <Textarea
                  id={`gateway-system-prompt-${index}`}
                  value={rule.text}
                  disabled={busy || !rule.enabled}
                  rows={6}
                  className="min-h-32 resize-y font-mono text-sm leading-6"
                  placeholder="输入要注入的系统提示词"
                  onChange={(event) => updateRule(index, { text: event.target.value })}
                />
              </div>

              <div className="grid gap-4 md:grid-cols-2">
                <div className="space-y-2">
                  <Label>生效范围</Label>
                  <div className="flex h-9 rounded-md border border-input bg-muted/25 p-0.5">
                    <button
                      type="button"
                      aria-pressed={!scopedKey}
                      disabled={busy || !rule.enabled}
                      onClick={() => updateRule(index, { key_scope: "all", key_ids: [] })}
                      className={cn(
                        "flex min-w-0 flex-1 items-center justify-center rounded px-2 text-xs font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50",
                        !scopedKey
                          ? "bg-background text-foreground shadow-xs"
                          : "text-muted-foreground hover:text-foreground",
                      )}
                    >
                      全部 Key
                    </button>
                    <button
                      type="button"
                      aria-pressed={scopedKey}
                      disabled={busy || !rule.enabled || keys.length === 0}
                      title={keys.length === 0 ? "请先创建 API Key" : undefined}
                      onClick={() =>
                        updateSelectedKeys(index, selectedKeys.length > 0 ? selectedKeys : [keys[0].id])
                      }
                      className={cn(
                        "flex min-w-0 flex-1 items-center justify-center gap-1 rounded px-2 text-xs font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50",
                        scopedKey
                          ? "bg-background text-foreground shadow-xs"
                          : "text-muted-foreground hover:text-foreground",
                      )}
                    >
                      <KeyRound className="size-3" /> 指定 Key
                    </button>
                  </div>

                  {scopedKey ? (
                    <Popover>
                      <PopoverTrigger asChild>
                        <Button
                          type="button"
                          variant="outline"
                          className="w-full justify-between font-normal"
                          disabled={busy || !rule.enabled || keys.length === 0}
                        >
                          <span className="flex min-w-0 items-center gap-2 truncate">
                            <KeyRound className="size-3.5 shrink-0 text-muted-foreground" />
                            {selectedKeys.length > 0
                              ? `已选 ${selectedKeys.length} 个 Key`
                              : "选择 API Key"}
                          </span>
                          <ChevronDown className="size-4 shrink-0 opacity-50" />
                        </Button>
                      </PopoverTrigger>
                      <PopoverContent
                        align="start"
                        className="w-[var(--radix-popover-trigger-width)] p-2"
                      >
                        <div className="flex items-center justify-between gap-2 px-1 pb-2 text-xs">
                          <span className="text-muted-foreground">选择生效的 API Key</span>
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            className="h-6 px-1.5 text-xs"
                            disabled={busy}
                            onClick={() =>
                              updateSelectedKeys(
                                index,
                                allKeysSelected ? [] : keys.map((key) => key.id),
                              )
                            }
                          >
                            {allKeysSelected ? "取消全选" : "全选"}
                          </Button>
                        </div>
                        <div className="max-h-56 space-y-1 overflow-y-auto">
                          {keys.map((key) => {
                            const checked = selectedKeys.includes(key.id)
                            return (
                              <label
                                key={key.id}
                                className="flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-sm hover:bg-muted"
                              >
                                <Checkbox
                                  checked={checked}
                                  disabled={busy}
                                  onCheckedChange={(next) =>
                                    updateSelectedKeys(
                                      index,
                                      next === true
                                        ? [...selectedKeys, key.id]
                                        : selectedKeys.filter((keyID) => keyID !== key.id),
                                    )
                                  }
                                />
                                <span className="min-w-0 flex-1 truncate">
                                  {key.name}{" "}
                                  <span className="font-mono text-[11px] text-muted-foreground">
                                    ({key.key_prefix})
                                  </span>
                                </span>
                              </label>
                            )
                          })}
                        </div>
                      </PopoverContent>
                    </Popover>
                  ) : null}
                </div>

                <div className="flex items-center justify-between gap-4 rounded-md border border-border bg-muted/20 px-3 py-3">
                  <div className="min-w-0">
                    <div className="text-sm font-medium">拼接已有提示词</div>
                    <p className="mt-1 text-[11px] leading-4 text-muted-foreground">
                      开启后，此条提示词会添加到客户端系统提示词之前。
                    </p>
                  </div>
                  <Switch
                    checked={rule.override}
                    disabled={busy || !rule.enabled}
                    onCheckedChange={(override) => updateRule(index, { override })}
                  />
                </div>
              </div>
            </section>
          )
        })}

        {rules.length === 0 ? (
          <div className="px-4 py-10 text-center text-sm text-muted-foreground">
            暂无提示词规则
          </div>
        ) : null}
      </div>

      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border px-4 py-4 sm:px-5">
        <Button
          type="button"
          variant="outline"
          disabled={busy}
          onClick={() => onChange([...rules, emptyRule()])}
        >
          <Plus className="size-3.5" /> 新增提示词
        </Button>
        <Button type="button" disabled={busy} onClick={onSave}>
          <Save className="size-3.5" /> 保存配置
        </Button>
      </div>
    </div>
  )
}
