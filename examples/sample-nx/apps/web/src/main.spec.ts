import { render } from './main';

describe('render', () => {
  it('formats the total', () => {
    expect(render(7)).toBe('total: 7');
  });
});
