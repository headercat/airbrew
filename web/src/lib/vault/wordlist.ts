// Compact word list for the passphrase generator.
//
// 128 short, memorable, unambiguous English nouns. 128 entries gives ~7 bits
// per word, so a 5-word passphrase is ~35 bits and a 7-word passphrase is ~49
// bits — comparable to a randomly generated 8–12 character password while
// being far easier to type and remember. Kept in its own module so the crypto
// helpers stay small and the list is easy to audit or replace.

export const WORDLIST = [
  "apple", "river", "stone", "cloud", "torch", "frost", "ocean", "maple",
  "pearl", "cedar", "valley", "willow", "amber", "basil", "candle", "drift",
  "ember", "falcon", "garnet", "harbor", "iguana", "jasper", "kettle", "lagoon",
  "marble", "ninja", "orchid", "paddle", "quartz", "raven", "saddle", "thistle",
  "umber", "viper", "walnut", "yacht", "zebra", "anchor", "boulder", "compass",
  "dolphin", "eagle", "feather", "garden", "hammer", "island", "jungle", "knuckle",
  "lantern", "meadow", "needle", "onion", "pebble", "quill", "ribbon", "shadow",
  "tunnel", "utopia", "violin", "whisker", "yellow", "arrow", "beacon", "cliff",
  "denim", "echo", "fern", "ginger", "helmet", "ivory", "jacket", "kingdom",
  "lemon", "magnet", "north", "office", "pillow", "queen", "rocket", "salmon",
  "temple", "unicorn", "vortex", "window", "xenon", "yodel", "zephyr", "almond",
  "bravo", "cocoa", "delta", "elm", "fable", "globe", "haven", "index",
  "jolly", "kayak", "liver", "mango", "novel", "oasis", "plum", "quiz",
  "rustic", "spark", "trail", "ultra", "vault", "wheat", "yarn", "zinc",
  "aster", "breeze", "coral", "daisy", "field", "grove", "heart", "ivory",
  "jewel", "karma", "lily", "mint", "nest", "oat", "palm", "quest",
];
