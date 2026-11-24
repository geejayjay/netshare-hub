import type { AnyFieldApi } from '@tanstack/react-form'
import type { ComponentProps, ReactNode } from 'react'
import { InfoTip, Input, Label, Select } from '@/shared/ui'

/** Label + optional (i) hover info + control + compact hint/error. */
export function FormField({
  label,
  htmlFor,
  error,
  hint,
  info,
  children,
}: {
  label: string
  htmlFor: string
  error?: string
  hint?: string
  info?: ReactNode
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center gap-1.5">
        <Label htmlFor={htmlFor}>{label}</Label>
        {info && <InfoTip content={info} label={`About ${label}`} />}
      </div>
      {children}
      {error ? (
        <p id={`${htmlFor}-error`} role="alert" className="text-xs text-danger">
          {error}
        </p>
      ) : (
        hint && <p className="text-xs text-fg-subtle">{hint}</p>
      )}
    </div>
  )
}

/**
 * The message to show for a field: the client-side validation error once the
 * field has been touched, otherwise the error the server sent for it.
 */
function fieldMessage(field: AnyFieldApi, serverError?: string): string | undefined {
  const { errors, isTouched } = field.state.meta
  const first: unknown = errors[0]
  const client = typeof first === 'string' ? first : (first as { message?: string } | undefined)?.message
  return (isTouched && client) || serverError
}

interface BoundFieldProps {
  /** The field from <form.Field>. */
  field: AnyFieldApi
  label: string
  hint?: string
  info?: ReactNode
  /** Error for this field returned by the API (see fieldErrors()). */
  serverError?: string
}

/** A text input wired to a TanStack Form field. */
export function TextField({
  field,
  label,
  hint,
  info,
  serverError,
  ...input
}: BoundFieldProps & Omit<ComponentProps<'input'>, 'value' | 'onChange' | 'onBlur' | 'id' | 'name'>) {
  const error = fieldMessage(field, serverError)
  return (
    <FormField label={label} htmlFor={field.name} error={error} hint={hint} info={info}>
      <Input
        id={field.name}
        name={field.name}
        value={field.state.value ?? ''}
        onChange={(e) => field.handleChange(e.target.value)}
        onBlur={field.handleBlur}
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? `${field.name}-error` : undefined}
        {...input}
      />
    </FormField>
  )
}

/** A select wired to a TanStack Form field. */
export function SelectField({
  field,
  label,
  hint,
  info,
  serverError,
  options,
}: BoundFieldProps & { options: { value: string; label: string }[] }) {
  const error = fieldMessage(field, serverError)
  return (
    <FormField label={label} htmlFor={field.name} error={error} hint={hint} info={info}>
      <Select
        id={field.name}
        name={field.name}
        value={field.state.value ?? ''}
        onChange={(e) => field.handleChange(e.target.value)}
        onBlur={field.handleBlur}
        aria-invalid={error ? true : undefined}
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </Select>
    </FormField>
  )
}
