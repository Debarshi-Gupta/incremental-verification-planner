import { sum } from './main';

describe('sum', () => {
  it('adds values', () => {
    expect(sum([1, 2, 3])).toBe(6);
  });
});
