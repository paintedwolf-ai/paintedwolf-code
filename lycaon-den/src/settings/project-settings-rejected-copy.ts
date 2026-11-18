export const PROJECT_SETTINGS_REJECTED_COPY = {
  heading: "Not applied in this project",
  limits:
    "This project's limits.yaml could not be read, so the device's limits apply. Fix the file to change this project's limits here.",
  verify:
    "This project's verify.yaml could not be read, so no test command is declared. Fix the file to set one here.",
  review:
    "This project's review.yaml could not be read, so every edit asks for review until it is fixed. Fix the file to change this project's review paths here.",
} as const;
