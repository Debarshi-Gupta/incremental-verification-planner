const { add } = require("./math");

test("adds numbers", () => {
  expect(add(2, 3)).toBe(5);
});
