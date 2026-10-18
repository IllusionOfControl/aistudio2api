// maskedProxy masks passwords in proxy URLs
export function maskedProxy(value: string): string {
  return value.replace(/^([a-z][a-z0-9+.-]*:\/\/[^:@/]*):[^@/]*@/i, '$1:•••@')
}
