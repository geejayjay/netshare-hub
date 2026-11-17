// Layouts frame pages. They are built from shared code only and never import
// a feature: anything feature-specific (navigation, user menu) is passed in.
//
//   AppLayout   signed-in pages; arrangement chosen by the user (see shell/)
//   AuthLayout  signed-out pages (login, setup)
export { AppLayout } from './AppLayout'
export { AuthLayout } from './AuthLayout'
export { layoutVariants, type LayoutVariant } from './shell/Shell'
export { useLayoutStore } from './layout-store'
export type { NavItem } from './types'
