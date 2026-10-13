# home-manager module for beacon's t CLI and the journal helpers:
# reminders when a part of the journal is missing, the prompt cache for
# `t hook zsh-journal`, and a script for a KDE timer shortcut.
#
# The flake passes itself in as `self`, so the default package is the t
# of the same beacon revision.
self:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.programs.beacon;
  journal = cfg.journal;
  t = lib.getExe cfg.package;

  # Systemd user services and KDE shortcuts do not see
  # home.sessionVariables, so everything that runs t sets them itself.
  env = {
    BEACON_SERVER_URL = cfg.serverUrl;
    BEACON_TOKEN_FILE = cfg.tokenFile;
  };
  exportEnv = lib.concatStrings (
    lib.mapAttrsToList (name: value: "export ${name}=${lib.escapeShellArg value}\n") env
  );

  # Shows a notification when a part of today's journal is missing; its
  # button opens a terminal with `t journal`. Before 16:00 it asks about
  # the morning part, later about the evening part (Persistent=true may
  # run a missed evening nudge the next morning; then the morning counts).
  nudge = pkgs.writeShellApplication {
    name = "beacon-journal-nudge";
    runtimeInputs = [
      cfg.package
      pkgs.libnotify
    ];
    text = ''
      ${exportEnv}
      # Exit code 1 means a part is missing; anything else ends here.
      rc=0
      t journal status --quiet --write-cache || rc=$?
      [ "$rc" -eq 1 ] || exit 0

      cache="''${XDG_CACHE_HOME:-$HOME/.cache}/beacon/journal-open"
      missing=$(cat "$cache" 2>/dev/null || true)
      if [ "$(date +%H)" -lt 16 ]; then
        part=morning text="Wie hast du geschlafen?"
      else
        part=evening text="Wie war der Tag?"
      fi
      case " $missing " in
        *" $part "*) ;;
        *) exit 0 ;;
      esac

      action=$(notify-send --app-name=beacon --icon=view-calendar-tasks \
        --action=open=Eintragen --wait "Journal offen" "$text" || true)
      if [ "$action" = open ]; then
        exec ${lib.escapeShellArgs journal.terminal} ${t} journal
      fi
    '';
  };

  # For a KDE global shortcut: starts a focus timer, or asks what was
  # done and stops the running one. Cancel in the dialog keeps it running.
  timerToggle = pkgs.writeShellApplication {
    name = "beacon-timer-toggle";
    runtimeInputs = [
      cfg.package
      pkgs.libnotify
      pkgs.kdePackages.kdialog
    ];
    text = ''
      ${exportEnv}
      rc=0
      label=$(t timer status --label 2>/dev/null) || rc=$?
      case $rc in
        0)
          result=$(kdialog --title beacon --inputbox "Was geschafft?" "$label") || exit 0
          if [ -n "$result" ]; then
            msg=$(t timer stop "$result" 2>&1) || true
          else
            msg=$(t timer stop 2>&1) || true
          fi
          ;;
        1)
          msg=$(t timer start 2>&1) || true
          ;;
        *)
          msg=$(t timer status 2>&1) || true
          ;;
      esac
      notify-send --app-name=beacon --icon=chronometer "beacon" "$msg"
    '';
  };

  zshHook = pkgs.runCommand "beacon-zsh-journal-hook" { } ''
    ${t} hook zsh-journal > $out
  '';
in
{
  options.programs.beacon = {
    enable = lib.mkEnableOption "beacon's t CLI";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.t;
      defaultText = lib.literalExpression "beacon.packages.\${system}.t";
      description = "The t package.";
    };

    serverUrl = lib.mkOption {
      type = lib.types.str;
      example = "https://beacon.example.org";
      description = "URL of the beacon server (BEACON_SERVER_URL).";
    };

    tokenFile = lib.mkOption {
      type = lib.types.str;
      example = "/run/secrets/beacon-token";
      description = ''
        File holding this client's API token (BEACON_TOKEN_FILE). A path as
        a string, so the token stays out of the Nix store.
      '';
    };

    journal = {
      enable = lib.mkEnableOption "the journal reminders, the prompt cache and the KDE timer script";

      terminal = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [
          "konsole"
          "-e"
        ];
        example = [
          "ghostty"
          "-e"
        ];
        description = "Command that opens a terminal running the command appended to it.";
      };

      nudgeTimes = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [
          "09:45"
          "23:45"
        ];
        description = ''
          When to remind about a missing journal part (systemd OnCalendar
          times): before 16:00 about the morning, later about the evening.
        '';
      };

      zshIntegration = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Set BEACON_JOURNAL_OPEN in zsh while a part of today's journal is missing.";
      };

      starshipIntegration = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          Configure Starship's env_var module to show [journal offen]. With a
          custom `format`, add ''${env_var.BEACON_JOURNAL_OPEN} to it.
        '';
      };
    };
  };

  config = lib.mkIf cfg.enable (
    lib.mkMerge [
      {
        home.packages = [ cfg.package ];
        home.sessionVariables = env;
      }

      (lib.mkIf journal.enable {
        home.packages = [ timerToggle ];

        systemd.user.services.beacon-journal-nudge = {
          Unit.Description = "Remind about a missing part of the beacon journal";
          Service = {
            Type = "oneshot";
            ExecStart = lib.getExe nudge;
          };
        };
        systemd.user.timers.beacon-journal-nudge = {
          Unit.Description = "Remind about the beacon journal";
          Timer = {
            OnCalendar = journal.nudgeTimes;
            Persistent = true;
          };
          Install.WantedBy = [ "timers.target" ];
        };

        # Keeps the cache file of `t hook zsh-journal` up to date; the prompt
        # itself never asks the server.
        systemd.user.services.beacon-journal-cache = {
          Unit.Description = "Update the beacon journal prompt cache";
          Service = {
            Type = "oneshot";
            Environment = lib.mapAttrsToList (name: value: "${name}=${value}") env;
            ExecStart = "${t} journal status --quiet --write-cache";
            # 1: a part is missing, 2: the server could not be asked. Both
            # are normal answers, not failures of the unit.
            SuccessExitStatus = "1 2";
          };
        };
        systemd.user.timers.beacon-journal-cache = {
          Unit.Description = "Update the beacon journal prompt cache every 15 minutes";
          Timer = {
            OnCalendar = "*:0/15";
            OnStartupSec = "1min";
          };
          Install.WantedBy = [ "timers.target" ];
        };

        programs.zsh.initContent = lib.mkIf journal.zshIntegration ''
          source ${zshHook}
        '';

        programs.starship.settings = lib.mkIf journal.starshipIntegration {
          env_var.BEACON_JOURNAL_OPEN = {
            format = "[\\[$env_value\\]]($style) ";
            style = "bold yellow";
          };
        };
      })
    ]
  );
}
