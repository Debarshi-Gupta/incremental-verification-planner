export function sum(values: number[]): number {
  return values.reduce((acc, value) => acc + value, 0);
}
