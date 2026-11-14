const { label } = require("./label");

test("trims label text", () => {
  expect(label("  hi ")).toBe("hi");
});
