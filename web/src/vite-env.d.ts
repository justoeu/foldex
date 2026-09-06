/// <reference types="vite/client" />

declare module '*.css' {
  const content: string
  export default content
}

declare module '*.tsx?raw' {
  const source: string
  export default source
}

declare module '*.ts?raw' {
  const source: string
  export default source
}
